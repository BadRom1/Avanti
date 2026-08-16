package export

import (
	"fmt"
	"io"

	"codeberg.org/go-pdf/fpdf"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

// Mise en page du document. A4 paysage : les tableaux de pièces portent une
// dizaine de colonnes, le portrait les écraserait.
const (
	pdfFont       = "Helvetica"
	pdfMarginMM   = 10.0
	pdfRowHeight  = 6.0
	pdfTitleSize  = 16.0
	pdfHeaderSize = 8.5
	pdfBodySize   = 8.5
)

// Textes fixes du document. En français, en constantes locales : voir le
// commentaire de package dans format.go.
const (
	pdfTitre           = "Dossier d'assurance"
	pdfGenere          = "Généré le"
	pdfSectionFactures = "Factures"
	pdfSectionAcomptes = "Acomptes versés"
	pdfSectionTotaux   = "Totaux"
	pdfAucunePiece     = "Aucune pièce."
	pdfPiecesJointes   = "Pièces jointes :"
	pdfTotalEngage     = "Engagé (devis retenus)"
	pdfTotalFacture    = "Facturé"
	pdfTotalPaye       = "Payé"
	pdfTotalRembourse  = "Remboursé par l'assurance"
	pdfEuroSuffix      = " EUR"
)

// En-têtes des deux tableaux, alignés sur leurs largeurs de colonnes.
var (
	pdfFactureHeader = []string{"Devis", "Entreprise", "Numéro", "Date", "Montant", "Règlement", "Réglée le", "Assurance", "Envoyée le", "Remboursé", "Remb. le"}
	pdfFactureWidths = []float64{48, 40, 26, 18, 24, 20, 18, 24, 18, 24, 17}

	pdfAcompteHeader = []string{"Devis", "Entreprise", "Date", "Montant", "Moyen", "Assurance", "Envoyée le", "Remboursé", "Remb. le"}
	pdfAcompteWidths = []float64{60, 52, 18, 24, 24, 24, 18, 24, 17}
)

// PDF implémente [finance.ExportFormat] sur codeberg.org/go-pdf/fpdf — le
// successeur maintenu de gofpdf, en Go pur, sans dépendance système. C'est le
// format de l'assureur et de l'expert : un document qui se lit et s'imprime
// tel quel.
//
// Les polices sont les quatorze polices de base du format PDF, encodées en
// cp1252 : tout le texte passe par le traducteur Unicode de la bibliothèque
// (UnicodeTranslatorFromDescriptor), sans quoi les accents du français
// sortiraient en mojibake. cp1252 couvre le français entier ; un caractère
// hors de la page de code sort en approximation, jamais en octet cassé.
type PDF struct{}

// NewPDF construit le format PDF.
func NewPDF() *PDF {
	return &PDF{}
}

// ContentType rend le type MIME du rendu.
func (*PDF) ContentType() string {
	return "application/pdf"
}

// FileExtension rend l'extension de fichier, sans point.
func (*PDF) FileExtension() string {
	return "pdf"
}

// Write rend le dossier d'assurance en PDF.
func (*PDF) Write(w io.Writer, dossier finance.DossierAssurance) error {
	doc := fpdf.New("L", "mm", "A4", "")
	doc.SetMargins(pdfMarginMM, pdfMarginMM, pdfMarginMM)
	doc.SetAutoPageBreak(true, pdfMarginMM)
	doc.AddPage()

	// tr traduit l'UTF-8 du Go vers le cp1252 des polices de base — chaque
	// chaîne écrite dans le document passe par lui.
	tr := doc.UnicodeTranslatorFromDescriptor("")

	writePDFHead(doc, tr, dossier)
	writeFacturesSection(doc, tr, dossier.Factures)
	writeAcomptesSection(doc, tr, dossier.Acomptes)
	writeTotauxSection(doc, tr, dossier.Totaux)

	if err := doc.Output(w); err != nil {
		return fmt.Errorf("export : écriture du PDF : %w", err)
	}

	return nil
}

// writePDFHead écrit le titre, l'intitulé du chantier et la date de génération.
func writePDFHead(doc *fpdf.Fpdf, tr func(string) string, dossier finance.DossierAssurance) {
	doc.SetFont(pdfFont, "B", pdfTitleSize)
	doc.CellFormat(0, 9, tr(pdfTitre), "", 1, "L", false, 0, "")

	doc.SetFont(pdfFont, "", pdfBodySize+1)
	if dossier.Intitule != "" {
		doc.CellFormat(0, pdfRowHeight, tr(dossier.Intitule), "", 1, "L", false, 0, "")
	}
	doc.CellFormat(0, pdfRowHeight, tr(pdfGenere+" "+formatInstant(dossier.GeneratedAt)), "", 1, "L", false, 0, "")
	doc.Ln(3)
}

// writeSectionTitle écrit le titre d'une section de tableau.
func writeSectionTitle(doc *fpdf.Fpdf, tr func(string) string, title string) {
	doc.SetFont(pdfFont, "B", pdfTitleSize-4)
	doc.CellFormat(0, 8, tr(title), "", 1, "L", false, 0, "")
}

// writeTableHeader écrit la ligne d'en-tête d'un tableau.
func writeTableHeader(doc *fpdf.Fpdf, tr func(string) string, widths []float64, header []string) {
	doc.SetFont(pdfFont, "B", pdfHeaderSize)
	doc.SetFillColor(235, 235, 235)
	for i, label := range header {
		doc.CellFormat(widths[i], pdfRowHeight, tr(label), "1", 0, "L", true, 0, "")
	}
	doc.Ln(-1)
	doc.SetFont(pdfFont, "", pdfBodySize)
}

// ensureTableRoom ouvre une page neuve — en-tête de tableau compris — quand la
// ligne à venir n'aurait plus la place. Sans ce contrôle, la coupure serait
// celle du saut de page automatique : au milieu d'une ligne, sans en-tête, et
// le tableau deviendrait illisible à la page deux.
func ensureTableRoom(doc *fpdf.Fpdf, tr func(string) string, widths []float64, header []string) {
	_, pageHeight := doc.GetPageSize()
	if doc.GetY()+2*pdfRowHeight > pageHeight-pdfMarginMM {
		doc.AddPage()
		writeTableHeader(doc, tr, widths, header)
	}
}

// fitCell tronque un texte déjà traduit pour qu'il tienne dans sa colonne,
// avec une ellipse : une raison sociale interminable ne doit pas déborder sur
// la cellule voisine. La chaîne est en cp1252 — un octet par caractère — donc
// la coupe octet par octet ne casse aucun caractère.
func fitCell(doc *fpdf.Fpdf, translated string, width float64) string {
	// La marge interne des cellules de fpdf vaut une chasse de part et d'autre ;
	// deux millimètres la couvrent aux tailles de ce document.
	const padding = 2.0
	if doc.GetStringWidth(translated) <= width-padding {
		return translated
	}

	const ellipsis = "\x85" // « … » en cp1252, la page de code des polices de base.
	for translated != "" && doc.GetStringWidth(translated+ellipsis) > width-padding {
		translated = translated[:len(translated)-1]
	}

	return translated + ellipsis
}

// writeTableRow écrit une ligne de tableau. Tout est aligné à gauche, montants
// compris : le document est un dossier de pièces, pas un livre de comptes.
func writeTableRow(doc *fpdf.Fpdf, tr func(string) string, widths []float64, cells []string) {
	for i, cell := range cells {
		doc.CellFormat(widths[i], pdfRowHeight, fitCell(doc, tr(cell), widths[i]), "1", 0, "L", false, 0, "")
	}
	doc.Ln(-1)
}

// writePiecesRow écrit, sous une ligne, la liste de ses pièces jointes.
func writePiecesRow(doc *fpdf.Fpdf, tr func(string) string, pieces []finance.PieceJointe) {
	if len(pieces) == 0 {
		return
	}

	doc.SetFont(pdfFont, "I", pdfBodySize-0.5)
	doc.CellFormat(0, pdfRowHeight-1, tr("    "+pdfPiecesJointes+" "+formatPieces(pieces)), "", 1, "L", false, 0, "")
	doc.SetFont(pdfFont, "", pdfBodySize)
}

// writeFacturesSection écrit le tableau des factures, chaque ligne suivie de
// ses pièces jointes.
func writeFacturesSection(doc *fpdf.Fpdf, tr func(string) string, factures []finance.LigneFacture) {
	writeSectionTitle(doc, tr, pdfSectionFactures)
	if len(factures) == 0 {
		writeEmptySection(doc, tr)
		return
	}

	writeTableHeader(doc, tr, pdfFactureWidths, pdfFactureHeader)
	for _, ligne := range factures {
		ensureTableRoom(doc, tr, pdfFactureWidths, pdfFactureHeader)
		writeTableRow(doc, tr, pdfFactureWidths, []string{
			ligne.DevisLibelle,
			ligne.Entreprise,
			ligne.Numero,
			formatDate(ligne.Date),
			formatMontant(ligne.Montant) + pdfEuroSuffix,
			paiementLabel(ligne.Paiement),
			formatInstant(ligne.PaidAt),
			assuranceLabel(ligne.Assurance.Statut),
			formatInstant(ligne.Assurance.SentAt),
			pdfRembourse(ligne.Assurance),
			formatInstant(ligne.Assurance.RefundedAt),
		})
		writePiecesRow(doc, tr, ligne.Pieces)
	}
	doc.Ln(4)
}

// writeAcomptesSection écrit le tableau des acomptes.
func writeAcomptesSection(doc *fpdf.Fpdf, tr func(string) string, acomptes []finance.LigneAcompte) {
	writeSectionTitle(doc, tr, pdfSectionAcomptes)
	if len(acomptes) == 0 {
		writeEmptySection(doc, tr)
		return
	}

	writeTableHeader(doc, tr, pdfAcompteWidths, pdfAcompteHeader)
	for _, ligne := range acomptes {
		ensureTableRoom(doc, tr, pdfAcompteWidths, pdfAcompteHeader)
		writeTableRow(doc, tr, pdfAcompteWidths, []string{
			ligne.DevisLibelle,
			ligne.Entreprise,
			formatDate(ligne.Date),
			formatMontant(ligne.Montant) + pdfEuroSuffix,
			moyenLabel(ligne.Moyen),
			// L'accord suit la nature de la pièce : un acompte est envoyé,
			// une facture est envoyée.
			assuranceAcompteLabel(ligne.Assurance.Statut),
			formatInstant(ligne.Assurance.SentAt),
			pdfRembourse(ligne.Assurance),
			formatInstant(ligne.Assurance.RefundedAt),
		})
		writePiecesRow(doc, tr, ligne.Pieces)
	}
	doc.Ln(4)
}

// writeEmptySection dit qu'une section n'a rien à montrer — un dossier sans
// acompte reste un dossier, la section vide le dit plutôt que de disparaître.
func writeEmptySection(doc *fpdf.Fpdf, tr func(string) string) {
	doc.SetFont(pdfFont, "", pdfBodySize)
	doc.CellFormat(0, pdfRowHeight, tr(pdfAucunePiece), "", 1, "L", false, 0, "")
	doc.Ln(4)
}

// writeTotauxSection écrit les quatre cumuls du chantier.
func writeTotauxSection(doc *fpdf.Fpdf, tr func(string) string, totaux finance.TotauxDossier) {
	writeSectionTitle(doc, tr, pdfSectionTotaux)

	rows := []struct {
		label   string
		montant finance.Montant
	}{
		{pdfTotalEngage, totaux.Engage},
		{pdfTotalFacture, totaux.Facture},
		{pdfTotalPaye, totaux.Paye},
		{pdfTotalRembourse, totaux.Rembourse},
	}

	doc.SetFont(pdfFont, "", pdfBodySize+1)
	for _, row := range rows {
		doc.CellFormat(70, pdfRowHeight, tr(row.label), "1", 0, "L", false, 0, "")
		doc.CellFormat(40, pdfRowHeight, tr(formatMontant(row.montant)+pdfEuroSuffix), "1", 1, "R", false, 0, "")
	}
}

// pdfRembourse écrit le montant remboursé, ou rien tant que la pièce ne l'est
// pas — même raisonnement que la colonne CSV.
func pdfRembourse(suivi finance.SuiviAssurance) string {
	if suivi.Statut != finance.AssuranceRemboursee {
		return ""
	}
	return formatMontant(suivi.MontantRembourse) + pdfEuroSuffix
}
