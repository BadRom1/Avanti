package export

import (
	"encoding/csv"
	"fmt"
	"io"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

// En-têtes du tableau CSV. En français : c'est un document utilisateur, au même
// titre que les valeurs qu'il porte — voir le commentaire de package dans
// format.go pour la frontière avec le catalogue i18n de l'adapter web.
var csvHeader = []string{
	"Type",
	"Devis",
	"Entreprise",
	"Numéro",
	"Date",
	"Montant (EUR)",
	"Règlement",
	"Réglée le",
	"Assurance",
	"Envoyée le",
	"Remboursé (EUR)",
	"Remboursée le",
	"Pièces jointes",
}

// Valeurs de la colonne « Type », qui distingue les lignes du tableau et les
// lignes de totaux — toutes les lignes ont le même nombre de champs, condition
// pour qu'un tableur ou encoding/csv relise le fichier sans réglage.
const (
	csvTypeFacture        = "Facture"
	csvTypeAcompte        = "Acompte"
	csvTypeTotalEngage    = "Total engagé"
	csvTypeTotalFacture   = "Total facturé"
	csvTypeTotalPaye      = "Total payé"
	csvTypeTotalRembourse = "Total remboursé"
)

// csvBOM est la marque d'ordre d'octets UTF-8, écrite en tête du fichier :
// sans elle, Excel sous Windows ouvre le fichier en ANSI et les accents du
// document sortent en mojibake. Les autres tableurs l'ignorent proprement.
const csvBOM = "\xEF\xBB\xBF"

// CSV implémente [finance.ExportFormat] sur encoding/csv : le tableau des
// pièces à plat, une ligne par facture ou acompte, suivi des totaux. C'est le
// format du comptable et du tableur, là où le PDF est celui de l'assureur.
type CSV struct{}

// NewCSV construit le format CSV.
func NewCSV() *CSV {
	return &CSV{}
}

// ContentType rend le type MIME du rendu. Le charset est annoncé : le document
// porte des accents, et un tableur qui devine l'encodage devine parfois mal.
func (*CSV) ContentType() string {
	return "text/csv; charset=utf-8"
}

// FileExtension rend l'extension de fichier, sans point.
func (*CSV) FileExtension() string {
	return "csv"
}

// Write rend le dossier d'assurance en CSV.
func (*CSV) Write(w io.Writer, dossier finance.DossierAssurance) error {
	if _, err := io.WriteString(w, csvBOM); err != nil {
		return fmt.Errorf("export : écriture du BOM UTF-8 : %w", err)
	}

	writer := csv.NewWriter(w)

	rows := make([][]string, 0, 1+len(dossier.Factures)+len(dossier.Acomptes)+4)
	rows = append(rows, csvHeader)
	for _, ligne := range dossier.Factures {
		rows = append(rows, csvFactureRow(ligne))
	}
	for _, ligne := range dossier.Acomptes {
		rows = append(rows, csvAcompteRow(ligne))
	}
	rows = append(rows,
		csvTotalRow(csvTypeTotalEngage, dossier.Totaux.Engage),
		csvTotalRow(csvTypeTotalFacture, dossier.Totaux.Facture),
		csvTotalRow(csvTypeTotalPaye, dossier.Totaux.Paye),
		csvTotalRow(csvTypeTotalRembourse, dossier.Totaux.Rembourse),
	)

	if err := writer.WriteAll(rows); err != nil {
		return fmt.Errorf("export : écriture du CSV : %w", err)
	}

	return nil
}

// neutralizeCell désamorce l'injection de formule dans une cellule TEXTE.
//
// Un tableur qui ouvre le CSV interprète une cellule commençant par « = »,
// « + », « - », « @ », une tabulation ou un retour chariot comme une formule —
// et une entreprise saisie « =SOMME(...) » ou « =cmd|... » s'exécuterait chez
// le comptable. Le préfixe apostrophe est la neutralisation standard : le
// tableur l'avale et affiche le texte tel quel. C'est une propriété du FORMAT,
// donc elle vit ici, dans l'adapter CSV — le domaine stocke ce qui a été
// saisi, et le PDF, qui n'exécute rien, n'en a pas besoin. Elle ne s'applique
// qu'aux cellules de texte libre : les montants et dates sont formatés par ce
// package, jamais par une saisie.
func neutralizeCell(cell string) string {
	if cell == "" {
		return cell
	}
	switch cell[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + cell
	default:
		return cell
	}
}

// csvFactureRow met une facture sous la forme d'une ligne du tableau.
func csvFactureRow(ligne finance.LigneFacture) []string {
	return []string{
		csvTypeFacture,
		neutralizeCell(ligne.DevisLibelle),
		neutralizeCell(ligne.Entreprise),
		neutralizeCell(ligne.Numero),
		formatDate(ligne.Date),
		formatMontant(ligne.Montant),
		paiementLabel(ligne.Paiement),
		formatInstant(ligne.PaidAt),
		assuranceLabel(ligne.Assurance.Statut),
		formatInstant(ligne.Assurance.SentAt),
		csvRembourse(ligne.Assurance),
		formatInstant(ligne.Assurance.RefundedAt),
		neutralizeCell(formatPieces(ligne.Pieces)),
	}
}

// csvAcompteRow met un acompte sous la forme d'une ligne du tableau. Les
// colonnes propres aux factures — numéro, date de règlement — restent vides ;
// la colonne « Règlement » porte le moyen de paiement, puisqu'un acompte est un
// règlement par nature.
func csvAcompteRow(ligne finance.LigneAcompte) []string {
	return []string{
		csvTypeAcompte,
		neutralizeCell(ligne.DevisLibelle),
		neutralizeCell(ligne.Entreprise),
		"",
		formatDate(ligne.Date),
		formatMontant(ligne.Montant),
		moyenLabel(ligne.Moyen),
		"",
		assuranceAcompteLabel(ligne.Assurance.Statut),
		formatInstant(ligne.Assurance.SentAt),
		csvRembourse(ligne.Assurance),
		formatInstant(ligne.Assurance.RefundedAt),
		neutralizeCell(formatPieces(ligne.Pieces)),
	}
}

// csvTotalRow met un total sous la forme d'une ligne du tableau : le libellé
// dans la colonne « Type », la valeur dans la colonne des montants, le reste
// vide — même largeur que les lignes de données, pour que le fichier se relise
// d'un bloc.
func csvTotalRow(label string, montant finance.Montant) []string {
	row := make([]string, len(csvHeader))
	row[0] = label
	row[5] = formatMontant(montant)

	return row
}

// csvRembourse écrit le montant remboursé, ou rien tant que la pièce ne l'est
// pas : un zéro dans la colonne dirait « remboursée de zéro euro », ce qui
// n'est pas la même chose que « pas encore remboursée ».
func csvRembourse(suivi finance.SuiviAssurance) string {
	if suivi.Statut != finance.AssuranceRemboursee {
		return ""
	}
	return formatMontant(suivi.MontantRembourse)
}
