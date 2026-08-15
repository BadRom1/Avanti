package web

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

// Erreurs de saisie propres à l'interface. Elles ne remontent jamais telles
// quelles à l'écran : chacune est associée à un message du catalogue.
var (
	// errMontantVide signale un champ montant laissé vide.
	errMontantVide = errors.New("web : montant absent")
	// errMontantIllisible signale une saisie qui n'est pas un nombre décimal.
	errMontantIllisible = errors.New("web : montant illisible")
	// errMontantHorsBornes signale un montant nul, négatif ou démesuré.
	errMontantHorsBornes = errors.New("web : montant hors bornes")
	// errDateIllisible signale une date qui n'est ni au format du navigateur ni
	// au format français.
	errDateIllisible = errors.New("web : date illisible")
)

// Séparateurs de la notation française des nombres.
const (
	// groupSeparator est l'espace insécable étroite (U+202F), celle que
	// l'imprimerie française emploie entre les milliers. Insécable, un montant ne
	// se coupe donc jamais en fin de ligne.
	groupSeparator = "\u202f"
	// decimalSeparator est la virgule décimale.
	decimalSeparator = ","
	// maxEurosDigits borne la partie entière d'une saisie. La borne du domaine
	// ([devis.MaxMontant]) tranchera ensuite ; celle-ci n'est là que pour qu'un
	// collage de trente chiffres ne déborde pas l'entier avant d'être jugé.
	maxEurosDigits = 12
)

// parseMontant traduit une saisie en euros vers les centimes du domaine, sans
// jamais passer par un flottant.
//
// C'est le point de l'interface où la précision se perd le plus facilement :
// strconv.ParseFloat("11800.50") suivi d'une multiplication par cent rend
// 1180049 une fois sur deux selon l'arrondi, et personne ne s'en aperçoit avant
// de comparer un total au papier de l'artisan. La conversion est donc faite en
// arithmétique entière, sur les deux moitiés de la saisie.
//
// Les formes acceptées sont celles qu'une personne tape réellement :
// « 11800,50 », « 11 800.50 », « 11800 », avec ou sans espaces de groupement,
// virgule ou point, et un symbole € éventuel.
func parseMontant(raw string) (devis.Montant, error) {
	cleaned := strings.NewReplacer(
		" ", "", "\u00a0", "", "\u202f", "", "\u2009", "", "€", "", ".", decimalSeparator,
	).Replace(strings.TrimSpace(raw))

	if cleaned == "" {
		return 0, errMontantVide
	}

	entiers, centimes, err := splitDecimal(cleaned)
	if err != nil {
		return 0, err
	}

	montant := devis.Montant(entiers*100 + centimes)
	if !montant.Valid() {
		return 0, errMontantHorsBornes
	}

	return montant, nil
}

// splitDecimal découpe une saisie nettoyée en euros et centimes entiers.
func splitDecimal(cleaned string) (euros, centimes int64, err error) {
	entiers, decimales, hasDecimals := strings.Cut(cleaned, decimalSeparator)

	// « ,50 » vaut cinquante centimes : la partie entière peut manquer, mais pas
	// les deux à la fois.
	if entiers == "" && !hasDecimals {
		return 0, 0, errMontantIllisible
	}
	if entiers == "" {
		entiers = "0"
	}
	if !digitsOnly(entiers) || len(entiers) > maxEurosDigits {
		return 0, 0, errMontantIllisible
	}

	if !hasDecimals {
		decimales = "00"
	}
	// Une saisie à un chiffre après la virgule vaut des dizaines de centimes :
	// « 12,5 » est douze euros cinquante, pas douze euros cinq.
	if len(decimales) == 1 {
		decimales += "0"
	}
	if !digitsOnly(decimales) || len(decimales) != 2 {
		return 0, 0, errMontantIllisible
	}

	euros, convErr := strconv.ParseInt(entiers, 10, 64)
	if convErr != nil {
		return 0, 0, errMontantIllisible
	}
	centimes, convErr = strconv.ParseInt(decimales, 10, 64)
	if convErr != nil {
		return 0, 0, errMontantIllisible
	}

	return euros, centimes, nil
}

// digitsOnly dit si la chaîne n'est faite que de chiffres décimaux. Une chaîne
// vide n'en est pas une.
func digitsOnly(candidate string) bool {
	if candidate == "" {
		return false
	}
	for _, r := range candidate {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// formatMontant écrit un montant en centimes sous la forme « 11 800,50 ».
//
// Le symbole de la monnaie n'y figure pas : il est dans le catalogue de
// traductions, où sa place à droite ou à gauche du nombre relève de la langue.
func formatMontant(montant devis.Montant) string {
	euros, centimes := montant.Split()

	var sb strings.Builder
	if montant < 0 {
		sb.WriteString("-")
	}
	sb.WriteString(groupThousands(strconv.FormatInt(euros, 10)))
	sb.WriteString(decimalSeparator)
	if centimes < 10 {
		sb.WriteString("0")
	}
	sb.WriteString(strconv.FormatInt(centimes, 10))

	return sb.String()
}

// groupThousands insère le séparateur de milliers dans une suite de chiffres.
func groupThousands(digits string) string {
	if len(digits) <= 3 {
		return digits
	}

	var sb strings.Builder
	head := len(digits) % 3
	if head > 0 {
		sb.WriteString(digits[:head])
	}
	for i := head; i < len(digits); i += 3 {
		if sb.Len() > 0 {
			sb.WriteString(groupSeparator)
		}
		sb.WriteString(digits[i : i+3])
	}

	return sb.String()
}

// Formats de date manipulés par l'interface.
const (
	// dateInputLayout est ce qu'un champ <input type="date"> envoie, quel que
	// soit l'affichage que le navigateur en fait.
	dateInputLayout = "2006-01-02"
	// dateDisplayLayout est la notation française, celle qui s'affiche.
	dateDisplayLayout = "02/01/2006"
)

// parseDate lit une date de formulaire.
//
// Les deux formats sont acceptés : celui du navigateur, et la notation
// française — un champ date reste tapable à la main, par un navigateur qui ne
// gère pas le type, par un test, ou par un appel direct.
func parseDate(raw string) (time.Time, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return time.Time{}, errDateIllisible
	}

	for _, layout := range []string{dateInputLayout, dateDisplayLayout} {
		if parsed, err := time.Parse(layout, candidate); err == nil {
			return parsed.UTC(), nil
		}
	}

	return time.Time{}, errDateIllisible
}

// formatDate écrit une date en notation française. Une date nulle rend la chaîne
// vide plutôt que « 01/01/0001 », qui n'apprendrait rien à personne.
//
// Le retour en UTC n'est pas cosmétique : les dates sont stockées à minuit UTC
// mais pgx les relit dans le fuseau local du serveur. Sans ce recadrage, une
// machine à l'ouest de Greenwich afficherait la veille.
func formatDate(instant time.Time) string {
	if instant.IsZero() {
		return ""
	}
	return instant.UTC().Format(dateDisplayLayout)
}

// formatDateInput écrit une date pour un champ <input type="date">. Même
// recadrage UTC que formatDate : sans lui, ouvrir puis enregistrer un
// formulaire reculerait la date d'un jour à chaque passage.
func formatDateInput(instant time.Time) string {
	if instant.IsZero() {
		return ""
	}
	return instant.UTC().Format(dateInputLayout)
}

// parseValidityDays lit une durée de validité exprimée en jours.
//
// Le champ est facultatif : vide vaut « non renseignée », qui est le cas
// courant. Une valeur négative est refusée par le domaine, une valeur
// fantaisiste ici.
func parseValidityDays(raw string) (time.Duration, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return 0, nil
	}

	days, err := strconv.Atoi(candidate)
	if err != nil || days < 0 {
		return 0, errDateIllisible
	}

	return time.Duration(days) * 24 * time.Hour, nil
}

// formatValidityDays écrit une durée de validité en nombre de jours, ou la
// chaîne vide quand elle n'est pas renseignée.
func formatValidityDays(validity time.Duration) string {
	if validity <= 0 {
		return ""
	}
	return strconv.Itoa(int(validity.Hours() / 24))
}
