package export

// Le français de ce fichier est du contenu de document, pas de l'interface
// web : un export CSV ou PDF est un livrable autonome — il part chez le
// comptable ou l'assureur sans passer par un navigateur — et son vocabulaire
// vit donc ici, en constantes locales, comme les valeurs qu'il accompagne. Le
// catalogue i18n (locales/fr.json) appartient à l'adapter web : le partager
// coûterait un import entre familles d'adapters que R4 interdit, pour des
// libellés qui ne sont pas les mêmes textes que ceux des pages.

import (
	"strconv"
	"strings"
	"time"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

// Formats de date du document : la notation française, celle des pièces papier
// que le dossier accompagne.
const dateLayout = "02/01/2006"

// Libellés des valeurs du domaine. Une valeur inconnue — un statut ajouté au
// domaine sans son libellé — ressort telle quelle plutôt que vide : un dossier
// d'assurance qui perd une information vaut moins qu'un dossier qui la montre
// brute.
var (
	paiementLabels = map[finance.StatutPaiement]string{
		finance.PaiementImpayee: "Impayée",
		finance.PaiementPayee:   "Payée",
	}
	// assuranceLabels accorde au féminin : c'est une facture (ou une pièce)
	// qui est envoyée puis remboursée.
	assuranceLabels = map[finance.StatutAssurance]string{
		finance.AssuranceNonEnvoyee: "Non envoyée",
		finance.AssuranceEnvoyee:    "Envoyée",
		finance.AssuranceRemboursee: "Remboursée",
	}
	// assuranceAcompteLabels accorde au masculin : un acompte est envoyé puis
	// remboursé. Les valeurs stockées, elles, ne changent pas — seule la
	// présentation s'accorde.
	assuranceAcompteLabels = map[finance.StatutAssurance]string{
		finance.AssuranceNonEnvoyee: "Non envoyé",
		finance.AssuranceEnvoyee:    "Envoyé",
		finance.AssuranceRemboursee: "Remboursé",
	}
	moyenLabels = map[finance.MoyenPaiement]string{
		finance.MoyenVirement: "Virement",
		finance.MoyenCheque:   "Chèque",
		finance.MoyenEspeces:  "Espèces",
		finance.MoyenCarte:    "Carte",
	}
)

func paiementLabel(statut finance.StatutPaiement) string {
	if label, ok := paiementLabels[statut]; ok {
		return label
	}
	return statut.String()
}

func assuranceLabel(statut finance.StatutAssurance) string {
	if label, ok := assuranceLabels[statut]; ok {
		return label
	}
	return statut.String()
}

func assuranceAcompteLabel(statut finance.StatutAssurance) string {
	if label, ok := assuranceAcompteLabels[statut]; ok {
		return label
	}
	return statut.String()
}

func moyenLabel(moyen finance.MoyenPaiement) string {
	if label, ok := moyenLabels[moyen]; ok {
		return label
	}
	return moyen.String()
}

// formatMontant écrit un montant en euros, virgule décimale, sans séparateur
// de milliers : « 11800,50 ». La forme est délibérément brute — un CSV se
// recalcule dans un tableur, une espace de groupement y ferait du texte — et
// l'arithmétique est entière, comme partout : [finance.Montant.Split] rend les
// deux moitiés, aucun flottant n'existe dans ce package.
func formatMontant(montant finance.Montant) string {
	euros, centimes := montant.Split()

	var sb strings.Builder
	if montant < 0 {
		sb.WriteString("-")
	}
	sb.WriteString(strconv.FormatInt(euros, 10))
	sb.WriteString(",")
	if centimes < 10 {
		sb.WriteString("0")
	}
	sb.WriteString(strconv.FormatInt(centimes, 10))

	return sb.String()
}

// formatDate écrit une DATE CIVILE en notation française, ou la chaîne vide
// pour une date nulle — un acompte n'a pas de date de remboursement tant que
// l'assurance n'a rien versé.
//
// Les dates civiles — la date que porte la pièce — sont stockées à minuit UTC
// mais relues par pgx dans le fuseau local du serveur : sans recadrage, un hôte
// à l'ouest de Greenwich daterait de la veille chaque pièce du dossier remis à
// l'assureur.
//
// Un horodatage réel (règlement, envoi, remboursement) se met en forme avec
// [formatInstant] : le recadrage UTC le daterait de la veille à l'est de
// Greenwich, et c'est cette date-là que lit l'assureur.
func formatDate(instant time.Time) string {
	if instant.IsZero() {
		return ""
	}
	return instant.UTC().Format(dateLayout)
}

// formatInstant écrit le jour d'un HORODATAGE en notation française, dans le
// fuseau du serveur — voir [formatDate] pour le partage des rôles.
func formatInstant(instant time.Time) string {
	return formatInstantIn(instant, time.Local)
}

// formatInstantIn est [formatInstant] avec un fuseau explicite, la couture par
// laquelle les tests vérifient le recadrage sans toucher à `time.Local`.
func formatInstantIn(instant time.Time, loc *time.Location) string {
	if instant.IsZero() {
		return ""
	}
	return instant.In(loc).Format(dateLayout)
}

// formatPieces énumère les pièces jointes d'une ligne : « nom (catégorie) »,
// séparées par des points-virgules — le séparateur de champ du CSV étant la
// virgule, les deux ne se confondent pas.
func formatPieces(pieces []finance.PieceJointe) string {
	if len(pieces) == 0 {
		return ""
	}

	parts := make([]string, 0, len(pieces))
	for _, piece := range pieces {
		parts = append(parts, piece.FileName+" ("+piece.Category+")")
	}

	return strings.Join(parts, " ; ")
}
