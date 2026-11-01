package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"runapp/internal/config"
	"runapp/internal/models"
	"runapp/internal/push"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v86"
	stripeclient "github.com/stripe/stripe-go/v86/client"
)

func TestShopCurrentPrice(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	cases := []struct {
		name string
		p    models.ShopProduct
		want int64
	}{
		{"sans promotion", models.ShopProduct{PriceCents: 3500}, 3500},
		{"promotion sans fin", models.ShopProduct{PriceCents: 3500, PromoPriceCents: 2900}, 2900},
		{"promotion en cours", models.ShopProduct{PriceCents: 3500, PromoPriceCents: 2900, PromoEndsAt: &future}, 2900},
		{"promotion terminée", models.ShopProduct{PriceCents: 3500, PromoPriceCents: 2900, PromoEndsAt: &past}, 3500},
		{"promotion plus chère que le prix : ignorée", models.ShopProduct{PriceCents: 3500, PromoPriceCents: 4000}, 3500},
	}
	for _, c := range cases {
		if got := c.p.CurrentPriceCents(now); got != c.want {
			t.Errorf("%s : %d, attendu %d", c.name, got, c.want)
		}
	}
}

func TestShopSlug(t *testing.T) {
	for raw, want := range map[string]string{
		"T-shirt Métal":   "t-shirt-metal",
		"  Été 2026 !!  ": "ete-2026",
		"Déjà vu / NR":    "deja-vu-nr",
		"!!!":             "",
	} {
		if got := shopSlug(raw); got != want {
			t.Errorf("shopSlug(%q) = %q, attendu %q", raw, got, want)
		}
	}
}

func TestShopMergeItems(t *testing.T) {
	got := shopMergeItems([]shopCartItem{
		{ProductID: "a", Size: "M", Quantity: 2},
		{ProductID: "a", Size: "M", Quantity: 3},
		{ProductID: "a", Size: "L", Quantity: 0},
		{ProductID: "b", Size: "S", Quantity: -4},
		{ProductID: "c", Size: "S", Quantity: 500},
	})
	if len(got) != 2 || got[0].Quantity != 5 || got[1].ProductID != "c" || got[1].Quantity != shopMaxQtyPerLine {
		t.Fatalf("panier mal normalisé : %+v", got)
	}
}

func TestReorderShopMedia(t *testing.T) {
	media := []models.ShopMedia{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := reorderShopMedia(media, []string{"c", "inconnu", "a"})
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	// Un identifiant inconnu est ignoré, un média oublié n’est jamais perdu.
	if strings.Join(ids, ",") != "c,a,b" {
		t.Fatalf("ordre : %v", ids)
	}
}

func TestPublishingRequiresPriceImageAndSize(t *testing.T) {
	active := true
	base := func() *models.ShopProduct {
		return &models.ShopProduct{
			Name: "T-shirt", PriceCents: 3500,
			Sizes: []models.ShopSize{{Label: "M", Stock: 1}},
			Media: []models.ShopMedia{{ID: "x", Kind: models.ShopMediaImage}},
		}
	}
	if err := applyShopProductBody(base(), &adminShopProductBody{Active: &active}); err != nil {
		t.Fatalf("article complet refusé : %v", err)
	}
	noPrice := base()
	noPrice.PriceCents = 0
	if applyShopProductBody(noPrice, &adminShopProductBody{Active: &active}) == nil {
		t.Error("publié sans prix")
	}
	videoOnly := base()
	videoOnly.Media = []models.ShopMedia{{ID: "v", Kind: models.ShopMediaVideo}}
	if applyShopProductBody(videoOnly, &adminShopProductBody{Active: &active}) == nil {
		t.Error("publié sans image")
	}
	noSize := base()
	noSize.Sizes = nil
	if applyShopProductBody(noSize, &adminShopProductBody{Active: &active}) == nil {
		t.Error("publié sans taille")
	}
	promo := int64(4000)
	if applyShopProductBody(base(), &adminShopProductBody{PromoPriceCents: &promo}) == nil {
		t.Error("promotion plus chère que le prix acceptée")
	}
}

func TestOpeningSalesRequiresTermsAndCountry(t *testing.T) {
	if validateShopSettings(&models.ShopSettings{SalesOpen: true, Countries: []string{"FR"}}) == nil {
		t.Error("ventes ouvertes sans conditions de vente")
	}
	if validateShopSettings(&models.ShopSettings{SalesOpen: true, Terms: "CGV"}) == nil {
		t.Error("ventes ouvertes sans pays")
	}
	if validateShopSettings(&models.ShopSettings{Countries: []string{"France"}}) == nil {
		t.Error("pays invalide accepté")
	}
	s := models.ShopSettings{SalesOpen: true, Terms: "CGV", Countries: []string{" fr "}}
	if err := validateShopSettings(&s); err != nil || s.Countries[0] != "FR" {
		t.Errorf("réglages valides refusés : %v %v", err, s.Countries)
	}
}

/* —— Parcours complet : vraie base Mongo locale, Stripe simulé —— */

// fakeStripe répond à ce que la boutique appelle : création et lecture de session.
type fakeStripe struct {
	mu       sync.Mutex
	sessions map[string]map[string]any
	lastForm url.Values
	seq      int
	failNew  bool
}

func (f *fakeStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
		if f.failNew {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "refus simulé", "type": "invalid_request_error"}})
			return
		}
		_ = r.ParseForm()
		f.lastForm = r.PostForm
		f.seq++
		id := fmt.Sprintf("cs_test_%d", f.seq)
		sess := map[string]any{
			"id": id, "object": "checkout.session", "url": "https://stripe.test/" + id,
			"status": "open", "payment_status": "unpaid",
			"metadata": map[string]string{shopOrderMetadata: r.PostForm.Get("metadata[" + shopOrderMetadata + "]")},
		}
		f.sessions[id] = sess
		_ = json.NewEncoder(w).Encode(sess)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
		sess, ok := f.sessions[strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "inconnue"}})
			return
		}
		_ = json.NewEncoder(w).Encode(sess)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeStripe) markPaid(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[id]["status"] = "complete"
	f.sessions[id]["payment_status"] = "paid"
	f.sessions[id]["customer_details"] = map[string]any{"email": "client@example.test"}
	f.sessions[id]["collected_information"] = map[string]any{"shipping_details": map[string]any{
		"name":    "Alex Martin",
		"address": map[string]any{"line1": "1 rue de la Paix", "postal_code": "75002", "city": "Paris", "country": "FR"},
	}}
}

type shopEnv struct {
	h      *Handlers
	db     *store.DB
	stripe *fakeStripe
	srv    *httptest.Server
}

var shopEnvSeq atomic.Int64

func newShopEnv(t *testing.T) *shopEnv {
	t.Helper()
	uri := os.Getenv("SHOP_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("SHOP_TEST_MONGODB_URI absent : parcours boutique non testé")
	}
	name := fmt.Sprintf("shop_test_%d_%d", time.Now().UnixNano(), shopEnvSeq.Add(1))
	db, err := store.Connect(uri, name, store.ConnectOptions{})
	if err != nil {
		t.Fatalf("mongo : %v", err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })

	fake := &fakeStripe{sessions: map[string]map[string]any{}}
	stripeSrv := httptest.NewServer(fake)
	t.Cleanup(stripeSrv.Close)
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL:               stripe.String(stripeSrv.URL),
		LeveledLogger:     &stripe.LeveledLogger{Level: stripe.LevelNull},
		MaxNetworkRetries: stripe.Int64(0),
	})

	h := &Handlers{
		cfg:        &config.Config{FrontendURL: "http://localhost:3000", ShopMediaDir: t.TempDir()},
		db:         db,
		pushClient: push.New(""),
		stripe:     stripeclient.New("sk_test_x", &stripe.Backends{API: backend, Connect: backend, Uploads: backend}),
	}
	r := chi.NewRouter()
	h.mountShopForTest(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &shopEnv{h: h, db: db, stripe: fake, srv: srv}
}

// mountShopForTest : les routes de la boutique, sans le contrôle d’identité de l’admin.
func (h *Handlers) mountShopForTest(r chi.Router) {
	r.Post("/quote", h.PublicShopQuote)
	r.Post("/checkout", h.PublicShopCheckout)
	r.Get("/confirm", h.PublicShopOrderConfirm)
	r.Get("/media/{file}", h.PublicShopMedia)
	r.Post("/admin/{id}/media", h.AdminUploadShopMedia)
}

func (e *shopEnv) open(t *testing.T) {
	t.Helper()
	err := e.db.UpsertShopSettings(context.Background(), models.ShopSettings{
		SalesOpen: true, ShippingCents: 490, Countries: []string{"FR"}, Terms: "CGV",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *shopEnv) product(t *testing.T, stock int, promo int64) *models.ShopProduct {
	t.Helper()
	now := time.Now().UTC()
	p := &models.ShopProduct{
		Slug: fmt.Sprintf("t-%d", now.UnixNano()), Name: "T-shirt Test", Active: true,
		PriceCents: 3500, PromoPriceCents: promo,
		Sizes:     []models.ShopSize{{Label: "M", Stock: stock}},
		Media:     []models.ShopMedia{{ID: "x.webp", Kind: models.ShopMediaImage}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.CreateShopProduct(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *shopEnv) stock(t *testing.T, p *models.ShopProduct) int {
	t.Helper()
	got, err := e.db.GetShopProduct(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := got.StockFor("M")
	return s
}

func (e *shopEnv) checkout(t *testing.T, p *models.ShopProduct, qty int) (int, string) {
	t.Helper()
	body, _ := json.Marshal(shopCartBody{Items: []shopCartItem{{ProductID: p.ID.Hex(), Size: "M", Quantity: qty}}})
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/checkout", bytes.NewReader(body))
	// Chaque requête vient d’une adresse différente : ce test ne vise pas la limite anti-abus.
	req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.0.%d.%d", time.Now().UnixNano()%250, shopEnvSeq.Add(1)%250))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, strings.TrimPrefix(out.URL, "https://stripe.test/")
}

func (e *shopEnv) confirm(t *testing.T, session string) map[string]any {
	t.Helper()
	res, err := http.Get(e.srv.URL + "/confirm?session_id=" + session)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out
}

func (e *shopEnv) orderFromSession(t *testing.T, session string) *models.ShopOrder {
	t.Helper()
	e.stripe.mu.Lock()
	raw := e.stripe.sessions[session]["metadata"].(map[string]string)[shopOrderMetadata]
	e.stripe.mu.Unlock()
	sess := &stripe.CheckoutSession{Metadata: map[string]string{shopOrderMetadata: raw}}
	oid, ok := shopOrderIDFromSession(sess)
	if !ok {
		t.Fatalf("session sans commande : %q", raw)
	}
	o, err := e.db.GetShopOrder(context.Background(), oid)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestShopCheckoutPaysServerPrice(t *testing.T) {
	e := newShopEnv(t)
	e.open(t)
	p := e.product(t, 5, 2900)

	code, session := e.checkout(t, p, 2)
	if code != http.StatusOK || session == "" {
		t.Fatalf("paiement refusé : %d", code)
	}
	if got := e.stock(t, p); got != 3 {
		t.Errorf("stock réservé : %d, attendu 3", got)
	}
	f := e.stripe.lastForm
	if f.Get("mode") != "payment" {
		t.Errorf("mode : %q", f.Get("mode"))
	}
	if got := f.Get("line_items[0][price_data][unit_amount]"); got != "2900" {
		t.Errorf("prix envoyé à Stripe : %q, attendu le prix promotionnel du serveur", got)
	}
	if got := f.Get("shipping_options[0][shipping_rate_data][fixed_amount][amount]"); got != "490" {
		t.Errorf("livraison : %q", got)
	}
	if got := f.Get("shipping_address_collection[allowed_countries][0]"); got != "FR" {
		t.Errorf("pays : %q", got)
	}

	if out := e.confirm(t, session); out["paid"] != false {
		t.Errorf("non payée présentée comme payée : %v", out)
	}
	e.stripe.markPaid(session)
	if out := e.confirm(t, session); out["paid"] != true {
		t.Errorf("paiement non reconnu : %v", out)
	}
	// Le retour du client et le webhook traitent le même paiement : aucun effet en double.
	e.confirm(t, session)
	o := e.orderFromSession(t, session)
	if o.Status != models.ShopOrderPaid || o.TotalCents != 2*2900+490 {
		t.Errorf("commande : %s, total %d", o.Status, o.TotalCents)
	}
	if o.Email != "client@example.test" || o.Address == nil || o.Address.City != "Paris" {
		t.Errorf("coordonnées non enregistrées : %q %+v", o.Email, o.Address)
	}
	if got := e.stock(t, p); got != 3 {
		t.Errorf("stock après paiement : %d, attendu 3", got)
	}
}

func TestShopExpiredOrderReturnsStockOnce(t *testing.T) {
	e := newShopEnv(t)
	e.open(t)
	p := e.product(t, 2, 0)
	_, session := e.checkout(t, p, 2)
	o := e.orderFromSession(t, session)

	ctx := context.Background()
	e.h.expireShopOrder(ctx, o.ID)
	e.h.expireShopOrder(ctx, o.ID)
	if got := e.stock(t, p); got != 2 {
		t.Errorf("stock rendu : %d, attendu 2 (une seule fois)", got)
	}
	if o, _ := e.db.GetShopOrder(ctx, o.ID); o.Status != models.ShopOrderExpired {
		t.Errorf("état : %s", o.Status)
	}
}

func TestShopSweepKeepsPaidOrderAndFreesAbandonedOne(t *testing.T) {
	e := newShopEnv(t)
	e.open(t)
	p := e.product(t, 4, 0)
	_, paid := e.checkout(t, p, 1)
	_, abandoned := e.checkout(t, p, 1)
	e.stripe.markPaid(paid)
	e.stripe.mu.Lock()
	e.stripe.sessions[abandoned]["status"] = "expired"
	e.stripe.mu.Unlock()

	// Les deux réservations ont dépassé leur durée, sans qu’aucun webhook ne soit arrivé.
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Hour)
	for _, s := range []string{paid, abandoned} {
		o := e.orderFromSession(t, s)
		o.ExpiresAt = past
		if err := e.db.ForceShopOrderExpiryForTest(ctx, o.ID, past); err != nil {
			t.Fatal(err)
		}
	}
	e.h.sweepStaleShopOrders(ctx)

	if o := e.orderFromSession(t, paid); o.Status != models.ShopOrderPaid {
		t.Errorf("commande payée : %s", o.Status)
	}
	if o := e.orderFromSession(t, abandoned); o.Status != models.ShopOrderExpired {
		t.Errorf("commande abandonnée : %s", o.Status)
	}
	if got := e.stock(t, p); got != 3 {
		t.Errorf("stock : %d, attendu 3 (une vente, un abandon)", got)
	}
}

func TestShopStripeFailureReturnsStock(t *testing.T) {
	e := newShopEnv(t)
	e.open(t)
	p := e.product(t, 2, 0)
	e.stripe.failNew = true
	if code, _ := e.checkout(t, p, 1); code != http.StatusBadGateway {
		t.Errorf("code : %d", code)
	}
	if got := e.stock(t, p); got != 2 {
		t.Errorf("stock : %d, attendu 2", got)
	}
}

func TestShopCannotOversell(t *testing.T) {
	e := newShopEnv(t)
	e.open(t)
	p := e.product(t, 3, 0)

	var wg sync.WaitGroup
	var sold, refused atomic.Int64
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch code, _ := e.checkout(t, p, 1); code {
			case http.StatusOK:
				sold.Add(1)
			case http.StatusConflict:
				refused.Add(1)
			default:
				t.Errorf("code inattendu : %d", code)
			}
		}()
	}
	wg.Wait()
	if sold.Load() != 3 || refused.Load() != 9 {
		t.Errorf("%d vendus, %d refusés : attendu 3 et 9", sold.Load(), refused.Load())
	}
	if got := e.stock(t, p); got != 0 {
		t.Errorf("stock : %d", got)
	}
}

func TestShopClosedRefusesPayment(t *testing.T) {
	e := newShopEnv(t)
	p := e.product(t, 2, 0)
	if code, _ := e.checkout(t, p, 1); code != http.StatusForbidden {
		t.Errorf("code : %d, attendu 403", code)
	}
	if got := e.stock(t, p); got != 2 {
		t.Errorf("stock touché ventes fermées : %d", got)
	}
}

func TestShopCheckoutRateLimit(t *testing.T) {
	l := &shopCheckoutLimiter{hits: map[string][]time.Time{}}
	now := time.Now()
	for i := range shopCheckoutPerIP {
		if !l.allow("ip", now) {
			t.Fatalf("refusé à la tentative %d", i+1)
		}
	}
	if l.allow("ip", now) {
		t.Error("limite dépassée sans refus")
	}
	if !l.allow("autre", now) {
		t.Error("une autre adresse ne doit pas être bloquée")
	}
	if !l.allow("ip", now.Add(shopCheckoutWindow+time.Second)) {
		t.Error("la fenêtre écoulée doit rouvrir l’accès")
	}
}

func (e *shopEnv) upload(t *testing.T, p *models.ShopProduct, content []byte) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "image.png")
	_, _ = fw.Write(content)
	_ = mw.Close()
	res, err := http.Post(e.srv.URL+"/admin/"+p.ID.Hex()+"/media", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

func TestShopMediaUpload(t *testing.T) {
	e := newShopEnv(t)
	p := e.product(t, 1, 0)

	// Le nom et le type annoncés ne comptent pas : seul le contenu décide.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if code := e.upload(t, p, svg); code != http.StatusBadRequest {
		t.Errorf("SVG accepté : %d", code)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	if code := e.upload(t, p, png); code != http.StatusCreated {
		t.Fatalf("PNG refusé : %d", code)
	}
	got, err := e.db.GetShopProduct(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := got.Media[len(got.Media)-1]
	if last.Kind != models.ShopMediaImage || !strings.HasSuffix(last.ID, ".png") {
		t.Fatalf("média : %+v", last)
	}

	res, err := http.Get(e.srv.URL + "/media/" + last.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	served, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !bytes.Equal(served, png) {
		t.Errorf("média servi : %d, %d octets", res.StatusCode, len(served))
	}
	for _, name := range []string{"..%2f..%2fetc%2fpasswd", "x.svg", last.ID + ".exe"} {
		res, err := http.Get(e.srv.URL + "/media/" + name)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s servi : %d", name, res.StatusCode)
		}
	}
}
