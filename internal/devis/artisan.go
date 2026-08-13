package devis

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"
)

// Bornes des textes saisis. Elles ne défendent pas un format, elles bornent ce
// qu'une saisie peut faire stocker : un champ de formulaire n'a pas de limite
// naturelle, une colonne si.
const (
	maxEntrepriseLength = 200
	maxEmailLength      = 254
	maxTelephoneLength  = 40
)

// Artisan est l'entreprise consultée, réduite à ce qui permet de la joindre et
// de la reconnaître.
//
// C'est une valeur, pas une entité : elle n'a pas d'identifiant et se recopie
// dans la demande comme dans le devis. Le jour où un carnet d'adresses
// d'artisans deviendra utile, il naîtra ailleurs — ici, dupliquer le nom d'une
// entreprise sur deux consultations est exactement ce qu'on veut, parce que ce
// qui a été écrit sur un devis reçu ne doit pas changer quand une fiche est
// corrigée.
type Artisan struct {
	// Entreprise est la raison sociale, seul champ obligatoire.
	Entreprise string
	// Email est l'adresse de contact, facultative.
	Email string
	// Telephone est le numéro de contact, facultatif. Il n'est ni validé ni
	// reformaté : les numéros d'entreprise s'écrivent de trop de façons
	// légitimes — indicatif international, extension, mention « portable » —
	// pour qu'un format imposé rende service.
	Telephone string
}

// NormalizeArtisan met un artisan sous sa forme canonique et refuse ce qui ne
// peut pas être stocké : espaces superflus retirés, email en minuscules.
//
// La normalisation est faite ici, dans le domaine, pour la même raison que dans
// identity : c'est une ligne de Go testable sans PostgreSQL, et il n'y a plus
// deux endroits susceptibles de ne pas être d'accord sur ce que « la même
// entreprise » veut dire.
func NormalizeArtisan(raw Artisan) (Artisan, error) {
	entreprise := strings.Join(strings.Fields(raw.Entreprise), " ")
	if entreprise == "" {
		return Artisan{}, ErrEmptyEntreprise
	}
	if utf8.RuneCountInString(entreprise) > maxEntrepriseLength {
		return Artisan{}, fmt.Errorf("%w : nom d'entreprise de plus de %d caractères", ErrTextTooLong, maxEntrepriseLength)
	}

	email, err := normalizeArtisanEmail(raw.Email)
	if err != nil {
		return Artisan{}, err
	}

	telephone := strings.Join(strings.Fields(raw.Telephone), " ")
	if utf8.RuneCountInString(telephone) > maxTelephoneLength {
		return Artisan{}, fmt.Errorf("%w : téléphone de plus de %d caractères", ErrTextTooLong, maxTelephoneLength)
	}

	return Artisan{Entreprise: entreprise, Email: email, Telephone: telephone}, nil
}

// normalizeArtisanEmail accepte l'absence d'adresse et refuse une adresse
// malformée.
//
// L'exigence est plus basse que pour un compte : cette adresse n'ouvre aucun
// accès, elle sert à relancer une entreprise. On vérifie donc la forme sans
// exiger de point dans le domaine, ce qui laisse passer les intranets
// d'entreprise sans laisser passer les fautes de frappe visibles.
func normalizeArtisanEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", nil
	}
	if len(email) > maxEmailLength {
		return "", fmt.Errorf("%w : %d caractères, %d au maximum", ErrInvalidArtisanEmail, len(email), maxEmailLength)
	}

	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return "", fmt.Errorf("%w : %s", ErrInvalidArtisanEmail, raw)
	}

	return email, nil
}

// NormalizeArtisans normalise une liste d'artisans sollicités et écarte les
// doublons de raison sociale.
//
// Solliciter deux fois la même entreprise n'est pas une erreur de l'utilisateur
// qui mérite un refus : c'est une ligne saisie deux fois, qu'on retire en
// gardant la première. Une entrée entièrement vide est ignorée de même — un
// formulaire propose toujours plus de lignes qu'on n'en remplit.
func NormalizeArtisans(raw []Artisan) ([]Artisan, error) {
	artisans := make([]Artisan, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))

	for _, candidate := range raw {
		if isBlankArtisan(candidate) {
			continue
		}

		artisan, err := NormalizeArtisan(candidate)
		if err != nil {
			return nil, err
		}

		key := strings.ToLower(artisan.Entreprise)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}

		artisans = append(artisans, artisan)
	}

	return artisans, nil
}

// isBlankArtisan reconnaît une ligne de formulaire laissée vide.
func isBlankArtisan(artisan Artisan) bool {
	return strings.TrimSpace(artisan.Entreprise) == "" &&
		strings.TrimSpace(artisan.Email) == "" &&
		strings.TrimSpace(artisan.Telephone) == ""
}
