package identity

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"
)

// Bornes de validation.
const (
	// MinPasswordLength est le seul critère imposé au mot de passe, et
	// il porte sur le nombre de caractères.
	//
	// Aucune règle de composition n'est exigée — ni majuscule, ni chiffre, ni
	// caractère spécial. Ces règles réduisent l'espace de recherche réel plus
	// qu'elles ne l'augmentent, parce qu'elles poussent vers « Motdepasse1! »
	// plutôt que vers une phrase longue. Douze caractères libres valent mieux, et
	// c'est aussi la recommandation du NIST (SP 800-63B) comme de l'ANSSI.
	MinPasswordLength = 12
	// MaxPasswordLength borne le travail qu'une requête peut demander.
	// Le coût d'argon2id ne dépend pas de la longueur de l'entrée, mais recevoir
	// et transporter un « mot de passe » de plusieurs mégaoctets n'a aucun usage
	// légitime. La borne est haute exprès : elle ne gêne aucune phrase de passe.
	MaxPasswordLength = 1024
	// maxEmailLength est la limite de la partie adresse d'un email au sens
	// de la RFC 5321. C'est aussi celle que porte la contrainte SQL.
	maxEmailLength = 254
	// maxDisplayNameLength borne un champ purement décoratif.
	maxDisplayNameLength = 120
)

// NormalizeEmail met une adresse sous la forme unique qui sert d'identifiant de
// connexion : sans espaces autour, en minuscules.
//
// La normalisation est faite ici, dans le domaine, plutôt que déléguée à une
// comparaison insensible à la casse côté base. La règle devient une ligne de Go
// testable sans PostgreSQL, la colonne peut porter un index unique ordinaire, et
// il n'y a plus deux endroits susceptibles de ne pas être d'accord sur ce que
// « la même adresse » veut dire.
func NormalizeEmail(raw string) (string, error) {
	candidate := strings.ToLower(strings.TrimSpace(raw))

	if candidate == "" {
		return "", fmt.Errorf("%w : adresse vide", ErrInvalidEmail)
	}
	if len(candidate) > maxEmailLength {
		return "", fmt.Errorf("%w : %d caractères, %d au maximum", ErrInvalidEmail, len(candidate), maxEmailLength)
	}

	addr, err := mail.ParseAddress(candidate)
	if err != nil {
		return "", fmt.Errorf("%w : %s", ErrInvalidEmail, raw)
	}
	// mail.ParseAddress accepte « Bob <bob@exemple.fr> » et les guillemets dans
	// la partie locale. Un identifiant de connexion doit être exactement ce que
	// la personne a tapé, sinon deux saisies différentes ouvriraient le même
	// compte — et l'une des deux ne correspondrait pas à ce qui est stocké.
	if addr.Name != "" || addr.Address != candidate {
		return "", fmt.Errorf("%w : adresse seule attendue, sans nom ni guillemets", ErrInvalidEmail)
	}
	// ParseAddress accepte « bob@localhost ». Un compte se crée avec une adresse
	// joignable : exiger un point dans le domaine écarte la faute de frappe la
	// plus courante sans prétendre valider l'existence de la boîte.
	if _, domain, _ := strings.Cut(candidate, "@"); !strings.Contains(domain, ".") {
		return "", fmt.Errorf("%w : le domaine doit comporter un point", ErrInvalidEmail)
	}

	return candidate, nil
}

// NormalizeDisplayName retire les espaces superflus d'un nom d'affichage et
// refuse un nom vide.
func NormalizeDisplayName(raw string) (string, error) {
	name := strings.Join(strings.Fields(raw), " ")

	if name == "" {
		return "", ErrEmptyDisplayName
	}
	if utf8.RuneCountInString(name) > maxDisplayNameLength {
		return "", fmt.Errorf("%w : nom d'affichage de plus de %d caractères", ErrEmptyDisplayName, maxDisplayNameLength)
	}

	return name, nil
}

// CheckPassword applique la politique de mot de passe.
//
// Le mot de passe n'est ni rogné ni transformé : les espaces de début et de fin
// en font partie, et les retirer reviendrait à refuser silencieusement une phrase
// de passe que la personne a bien tapée deux fois.
func CheckPassword(password string) error {
	length := utf8.RuneCountInString(password)

	switch {
	case length < MinPasswordLength:
		return fmt.Errorf("%w : %d caractères, %d au minimum", ErrPasswordTooShort, length, MinPasswordLength)
	case len(password) > MaxPasswordLength:
		return fmt.Errorf("%w : %d octets, %d au maximum", ErrPasswordTooLong, len(password), MaxPasswordLength)
	default:
		return nil
	}
}
