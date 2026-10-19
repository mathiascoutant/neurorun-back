package models

import "strings"

// Le contrat type d’ambassadeur vit ici, côté serveur, et pas dans l’interface : c’est le
// texte qui engage les deux parties. Les clients l’affichent, ils ne l’écrivent pas — et
// la version signée (AmbassadorContractVersion) est enregistrée avec chaque signature.

// ContractBlock : un paragraphe, une liste à puces, ou les deux dans cet ordre.
type ContractBlock struct {
	Text    string   `json:"text,omitempty"`
	Bullets []string `json:"bullets,omitempty"`
}

type ContractArticle struct {
	Number int             `json:"number"`
	Title  string          `json:"title"`
	Blocks []ContractBlock `json:"blocks"`
}

// Contract : le contrat prêt à afficher, remise de l’ambassadeur déjà insérée.
type Contract struct {
	Version      string            `json:"version"`
	Title        string            `json:"title"`
	Subtitle     string            `json:"subtitle"`
	PartiesTitle string            `json:"parties_title"`
	PartiesIntro string            `json:"parties_intro"`
	Articles     []ContractArticle `json:"articles"`
	Closing      []string          `json:"closing"`
	Mention      string            `json:"mention"`
	Footer       string            `json:"footer"`
	// CompanyName : signataire côté NeuroRun, tel qu’il apparaît sous la signature.
	CompanyName string `json:"company_name"`
}

// BuildAmbassadorContract : le contrat type, avec la remise du code personnel écrite à
// l’article 2. Sans code attaché, la phrase reste générale plutôt que de mentir sur un taux.
func BuildAmbassadorContract(discountLabel string) Contract {
	discountLabel = strings.TrimSpace(discountLabel)
	benefit := "Un code de réduction personnel donnant droit à une remise aux personnes qui s’abonnent de la part de l’ambassadeur."
	if discountLabel != "" {
		benefit = "Un code de réduction personnel donnant droit à " + discountLabel +
			" de remise aux personnes qui s’abonnent de la part de l’ambassadeur."
	}

	return Contract{
		Version:      AmbassadorContractVersion,
		Title:        "Contrat d’ambassadeur",
		Subtitle:     "NeuroRun offre l’accès à son offre Performance et un code de réduction personnel ; l’ambassadeur utilise l’application et en parle sur ses réseaux.",
		PartiesTitle: "L’ambassadeur",
		PartiesIntro: "Entre NeuroRun, éditeur de l’application NeuroRun (neurorun.fr), et :",
		Articles: []ContractArticle{
			{
				Number: 1,
				Title:  "Durée",
				Blocks: []ContractBlock{
					{Text: "Le contrat prend effet à la date de signature pour une durée de six mois. Il est ensuite reconduit automatiquement par périodes de six mois, sauf si l’une des parties y met fin par message écrit au moins quinze jours avant l’échéance."},
					{Text: "Ce partenariat n’est pas rémunéré. Il ne crée ni contrat de travail, ni lien de subordination entre les parties."},
				},
			},
			{
				Number: 2,
				Title:  "Ce que NeuroRun apporte",
				Blocks: []ContractBlock{
					{Bullets: []string{
						"L’accès gratuit à l’offre Performance, la plus complète, pendant toute la durée du contrat et de ses reconductions.",
						benefit,
						"Un accès en avant-première aux nouveautés de l’application et du site : nouvelles fonctionnalités, offres et évolutions, avant leur mise à disposition du public.",
						"Les visuels de marque et les informations utiles à sa communication.",
					}},
				},
			},
			{
				Number: 3,
				Title:  "Ce que l’ambassadeur s’engage à faire",
				Blocks: []ContractBlock{
					{Bullets: []string{
						"Utiliser régulièrement l’application NeuroRun pour son entraînement.",
						"Publier au minimum deux stories et une publication par mois montrant son utilisation de l’application, en mentionnant le compte @neurorun_officiel et son code de réduction.",
						"Ne mettre en avant aucune autre application de course à pied, de suivi d’entraînement ou de coaching dans ses stories et publications pendant la durée du contrat : les contenus liés à l’entraînement sont consacrés à NeuroRun.",
						"Parler de l’application de façon sincère, sans promesse de résultat, et ne tenir aucun propos portant atteinte à l’image de NeuroRun.",
					}},
					{Text: "L’ambassadeur autorise NeuroRun à reprendre ses publications relatives à l’application sur les comptes de la marque. Cette autorisation peut être retirée à tout moment par simple message."},
				},
			},
			{
				Number: 4,
				Title:  "Fin du contrat",
				Blocks: []ContractBlock{
					{Text: "Chaque partie peut mettre fin au contrat à tout moment par message écrit, avec un préavis de quinze jours et sans indemnité. NeuroRun peut y mettre fin sans préavis en cas de manquement grave, notamment un usage frauduleux du code de réduction ou l’absence totale de publication pendant deux mois."},
					{Text: "À la fin du contrat, le code de réduction est désactivé et le compte bascule vers l’offre gratuite, sans perte des données d’entraînement. Le présent contrat est soumis au droit français."},
				},
			},
		},
		Closing:     []string{"Fait en deux exemplaires."},
		Mention:     AmbassadorMention,
		Footer:      "NeuroRun — neurorun.fr — @neurorun_officiel",
		CompanyName: "NeuroRun",
	}
}
