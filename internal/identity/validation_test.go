package identity_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/identity"
)

func TestNormalizeEmailAccepts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "adresse simple", raw: "romain@exemple.fr", want: "romain@exemple.fr"},
		{name: "casse ramenée en minuscules", raw: "Romain.Badino@Exemple.FR", want: "romain.badino@exemple.fr"},
		{name: "espaces autour retirés", raw: "  romain@exemple.fr\t", want: "romain@exemple.fr"},
		{name: "sous-adressage conservé", raw: "romain+avanti@exemple.fr", want: "romain+avanti@exemple.fr"},
		{name: "sous-domaine", raw: "romain@courrier.exemple.fr", want: "romain@courrier.exemple.fr"},
		{name: "tiret dans le domaine", raw: "contact@next-level.run", want: "contact@next-level.run"},
		{
			// La borne exacte de la RFC 5321, celle que porte aussi la contrainte SQL.
			// L'accepter est ce qui distingue « au maximum 254 » de « moins de 254 ».
			name: "254 caractères, la borne exacte",
			raw:  strings.Repeat("a", 254-len("@exemple.fr")) + "@exemple.fr",
			want: strings.Repeat("a", 254-len("@exemple.fr")) + "@exemple.fr",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := identity.NormalizeEmail(tc.raw)
			if err != nil {
				t.Fatalf("NormalizeEmail(%q) échoué : %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeEmail(%q) = %q, attendu %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeEmailRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{name: "vide", raw: ""},
		{name: "espaces seuls", raw: "   "},
		{name: "sans arobase", raw: "romain.exemple.fr"},
		{name: "sans partie locale", raw: "@exemple.fr"},
		{name: "sans domaine", raw: "romain@"},
		{name: "domaine sans point", raw: "romain@localhost"},
		{name: "deux arobases", raw: "romain@exemple@fr"},
		{name: "avec nom d'affichage", raw: "Romain <romain@exemple.fr>"},
		{name: "partie locale entre guillemets", raw: `"romain badino"@exemple.fr`},
		{name: "espace au milieu", raw: "romain badino@exemple.fr"},
		{name: "trop long", raw: strings.Repeat("a", 250) + "@exemple.fr"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := identity.NormalizeEmail(tc.raw)
			if !errors.Is(err, identity.ErrInvalidEmail) {
				t.Errorf("NormalizeEmail(%q) = (%q, %v), attendu ErrInvalidEmail", tc.raw, got, err)
			}
		})
	}
}

// TestNormalizeEmailIsIdempotent : la forme normalisée d'une adresse déjà
// normalisée est elle-même. Sans cela, une adresse stockée pourrait ne plus
// correspondre à sa propre normalisation au tour suivant.
func TestNormalizeEmailIsIdempotent(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"Romain@Exemple.FR", "  contact@next-level.run  ", "a+b@c.de"} {
		first, err := identity.NormalizeEmail(raw)
		if err != nil {
			t.Fatalf("NormalizeEmail(%q) échoué : %v", raw, err)
		}
		second, err := identity.NormalizeEmail(first)
		if err != nil {
			t.Fatalf("NormalizeEmail(%q) échoué au second passage : %v", first, err)
		}
		if first != second {
			t.Errorf("normalisation non idempotente : %q puis %q", first, second)
		}
	}
}

func TestNormalizeDisplayName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "nom simple", raw: "Romain Badino", want: "Romain Badino"},
		{name: "espaces autour", raw: "  Romain  ", want: "Romain"},
		{name: "espaces internes réduits", raw: "Romain   Badino", want: "Romain Badino"},
		{name: "tabulations et retours à la ligne", raw: "Romain\t\nBadino", want: "Romain Badino"},
		{name: "accents conservés", raw: "Amélie Dupré", want: "Amélie Dupré"},
		{
			// La borne exacte, comptée en runes : 120 caractères accentués font 240
			// octets, et les compter en octets refuserait ce nom parfaitement valide.
			name: "120 caractères, la borne exacte",
			raw:  strings.Repeat("é", 120),
			want: strings.Repeat("é", 120),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := identity.NormalizeDisplayName(tc.raw)
			if err != nil {
				t.Fatalf("NormalizeDisplayName(%q) échoué : %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeDisplayName(%q) = %q, attendu %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeDisplayNameRejects(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "   ", "\t\n", strings.Repeat("é", 121)} {
		if _, err := identity.NormalizeDisplayName(raw); !errors.Is(err, identity.ErrEmptyDisplayName) {
			t.Errorf("NormalizeDisplayName(%q) = %v, attendu ErrEmptyDisplayName", raw, err)
		}
	}
}

func TestCheckPassword(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		password string
		want     error
	}{
		{name: "vide", password: "", want: identity.ErrPasswordTooShort},
		{name: "onze caractères", password: strings.Repeat("a", 11), want: identity.ErrPasswordTooShort},
		{name: "douze caractères, la borne exacte", password: strings.Repeat("a", 12), want: nil},
		{name: "phrase de passe", password: "le chantier avance doucement", want: nil},
		{
			name: "douze caractères comptés en runes, pas en octets",
			// Douze caractères accentués font vingt-quatre octets : compter les
			// octets accepterait ici une phrase de six caractères ailleurs.
			password: strings.Repeat("é", 12),
			want:     nil,
		},
		{
			name:     "onze runes accentuées restent trop courtes",
			password: strings.Repeat("é", 11),
			want:     identity.ErrPasswordTooShort,
		},
		{name: "un kilooctet, la borne haute exacte", password: strings.Repeat("a", 1024), want: nil},
		{name: "au-delà du kilooctet", password: strings.Repeat("a", 1025), want: identity.ErrPasswordTooLong},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := identity.CheckPassword(tc.password)
			switch {
			case tc.want == nil && err != nil:
				t.Errorf("CheckPassword() = %v, attendu nil", err)
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Errorf("CheckPassword() = %v, attendu %v", err, tc.want)
			}
		})
	}
}

// TestCheckPasswordRequiresNoComposition est un test de non-régression sur
// une décision : la politique porte sur la longueur, pas sur la composition. Un
// contributeur qui ajouterait « au moins une majuscule » le verrait échouer.
func TestCheckPasswordRequiresNoComposition(t *testing.T) {
	t.Parallel()

	valid := []string{
		"aaaaaaaaaaaa",
		"111111111111",
		"            ",
		"le chantier avance",
		"########------####",
	}

	for _, password := range valid {
		if err := identity.CheckPassword(password); err != nil {
			t.Errorf("CheckPassword(%q) = %v, aucune règle de composition n'est imposée", password, err)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)

	for range 50 {
		password, err := identity.GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword() échoué : %v", err)
		}
		if err := identity.CheckPassword(password); err != nil {
			t.Fatalf("GeneratePassword() a produit %q, refusé par la politique : %v", password, err)
		}
		if seen[password] {
			t.Fatalf("GeneratePassword() a rendu deux fois %q", password)
		}
		seen[password] = true
	}
}

// TestNewIDIsUnique : deux comptes ne doivent jamais partager un
// identifiant, et la forme reste celle d'un UUID canonique.
func TestNewIDIsUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[identity.ID]bool)

	for range 200 {
		id, err := identity.NewID()
		if err != nil {
			t.Fatalf("NewID() échoué : %v", err)
		}
		if seen[id] {
			t.Fatalf("NewID() a rendu deux fois %s", id)
		}
		seen[id] = true

		if len(id) != 36 {
			t.Fatalf("NewID() = %q, 36 caractères attendus", id)
		}
		// Version 4, variante RFC 4122 : les deux quartets imposés par la norme.
		if id[14] != '4' {
			t.Errorf("NewID() = %q, le chiffre de version doit être 4", id)
		}
		if !strings.ContainsRune("89ab", rune(id[19])) {
			t.Errorf("NewID() = %q, le quartet de variante doit valoir 8, 9, a ou b", id)
		}
	}
}
