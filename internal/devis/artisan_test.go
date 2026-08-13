package devis_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

func TestNormalizeArtisan(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		raw  devis.Artisan
		want devis.Artisan
	}{
		"les blancs superflus partent": {
			raw:  devis.Artisan{Entreprise: "  Charpentes   du Val  ", Email: " Contact@Val.fr ", Telephone: " 04 78  00 00 00 "},
			want: devis.Artisan{Entreprise: "Charpentes du Val", Email: "contact@val.fr", Telephone: "04 78 00 00 00"},
		},
		"l'email est facultatif": {
			raw:  devis.Artisan{Entreprise: "Toiture Ain", Telephone: "0478000000"},
			want: devis.Artisan{Entreprise: "Toiture Ain", Telephone: "0478000000"},
		},
		"le téléphone est facultatif": {
			raw:  devis.Artisan{Entreprise: "Toiture Ain", Email: "devis@toiture-ain.fr"},
			want: devis.Artisan{Entreprise: "Toiture Ain", Email: "devis@toiture-ain.fr"},
		},
		// Un intranet d'entreprise n'a pas toujours de point dans son domaine :
		// cette adresse ne donne aucun accès, l'exigence reste plus basse que
		// pour un compte.
		"un domaine sans point passe": {
			raw:  devis.Artisan{Entreprise: "Toiture Ain", Email: "devis@intranet"},
			want: devis.Artisan{Entreprise: "Toiture Ain", Email: "devis@intranet"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := devis.NormalizeArtisan(tc.raw)
			if err != nil {
				t.Fatalf("NormalizeArtisan() échoué : %v", err)
			}
			if got != tc.want {
				t.Errorf("NormalizeArtisan() = %+v, attendu %+v", got, tc.want)
			}
		})
	}
}

func TestNormalizeArtisanRejects(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		raw  devis.Artisan
		want error
	}{
		"entreprise vide":            {raw: devis.Artisan{Entreprise: ""}, want: devis.ErrEmptyEntreprise},
		"entreprise faite d'espaces": {raw: devis.Artisan{Entreprise: "   \t "}, want: devis.ErrEmptyEntreprise},
		"entreprise trop longue": {
			raw:  devis.Artisan{Entreprise: strings.Repeat("é", 201)},
			want: devis.ErrTextTooLong,
		},
		"téléphone trop long": {
			raw:  devis.Artisan{Entreprise: "Toiture Ain", Telephone: strings.Repeat("0", 41)},
			want: devis.ErrTextTooLong,
		},
		"email sans arobase": {
			raw:  devis.Artisan{Entreprise: "Toiture Ain", Email: "devis-toiture-ain.fr"},
			want: devis.ErrInvalidArtisanEmail,
		},
		"email avec nom d'affichage": {
			raw:  devis.Artisan{Entreprise: "Toiture Ain", Email: "Devis <devis@toiture-ain.fr>"},
			want: devis.ErrInvalidArtisanEmail,
		},
		"email trop long": {
			raw:  devis.Artisan{Entreprise: "Toiture Ain", Email: strings.Repeat("a", 250) + "@val.fr"},
			want: devis.ErrInvalidArtisanEmail,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := devis.NormalizeArtisan(tc.raw); !errors.Is(err, tc.want) {
				t.Errorf("NormalizeArtisan() = %v, attendu %v", err, tc.want)
			}
		})
	}
}

// TestNormalizeArtisansSkipsBlanksAndDuplicates : un formulaire propose toujours
// plus de lignes qu'on n'en remplit, et la même entreprise saisie deux fois est
// une ligne en trop, pas une erreur qui mérite de refuser toute la demande.
func TestNormalizeArtisansSkipsBlanksAndDuplicates(t *testing.T) {
	t.Parallel()

	artisans, err := devis.NormalizeArtisans([]devis.Artisan{
		{Entreprise: "Charpentes du Val", Email: "contact@val.fr"},
		{Entreprise: "  ", Email: "  ", Telephone: " "},
		{Entreprise: "charpentes du val", Email: "autre@val.fr"},
		{Entreprise: "Toiture Ain"},
	})
	if err != nil {
		t.Fatalf("NormalizeArtisans() échoué : %v", err)
	}

	if len(artisans) != 2 {
		t.Fatalf("NormalizeArtisans() a rendu %d artisans, attendu 2 : %+v", len(artisans), artisans)
	}
	// Le doublon écarté est le second : la première saisie fait foi, avec son
	// adresse.
	if artisans[0].Email != "contact@val.fr" {
		t.Errorf("le doublon a écrasé la première saisie : %+v", artisans[0])
	}
	if artisans[1].Entreprise != "Toiture Ain" {
		t.Errorf("artisans[1] = %+v, attendu Toiture Ain", artisans[1])
	}
}

func TestNormalizeArtisansPropagatesError(t *testing.T) {
	t.Parallel()

	_, err := devis.NormalizeArtisans([]devis.Artisan{
		{Entreprise: "Charpentes du Val"},
		{Entreprise: "Toiture Ain", Email: "pas-une-adresse"},
	})
	if !errors.Is(err, devis.ErrInvalidArtisanEmail) {
		t.Errorf("NormalizeArtisans() = %v, attendu %v", err, devis.ErrInvalidArtisanEmail)
	}
}

func TestNormalizeLot(t *testing.T) {
	t.Parallel()

	if got, err := devis.NormalizeLot("  Charpente   et couverture "); err != nil || got != "Charpente et couverture" {
		t.Errorf("NormalizeLot() = (%q, %v), attendu (%q, nil)", got, err, "Charpente et couverture")
	}

	if _, err := devis.NormalizeLot("   "); !errors.Is(err, devis.ErrEmptyLot) {
		t.Errorf("NormalizeLot(vide) = %v, attendu %v", err, devis.ErrEmptyLot)
	}

	if _, err := devis.NormalizeLot(strings.Repeat("a", 121)); !errors.Is(err, devis.ErrTextTooLong) {
		t.Errorf("NormalizeLot(trop long) = %v, attendu %v", err, devis.ErrTextTooLong)
	}

	if _, err := devis.NormalizeLot(strings.Repeat("a", 120)); err != nil {
		t.Errorf("NormalizeLot(120 caractères) = %v, la borne doit être inclusive", err)
	}
}

// TestDemandeArtisanLookup : retrouver une entreprise sollicitée ne doit pas
// dépendre de la casse ni des espaces, parce que c'est ce que l'interface fera
// en pré-remplissant le formulaire d'un devis reçu.
func TestDemandeArtisanLookup(t *testing.T) {
	t.Parallel()

	demande := devis.DemandeDevis{
		Artisans: []devis.Artisan{
			{Entreprise: "Charpentes du Val", Email: "contact@val.fr"},
			{Entreprise: "Toiture Ain"},
		},
	}

	found, ok := demande.Artisan("  charpentes DU val ")
	if !ok {
		t.Fatal("Artisan() n'a pas retrouvé une entreprise sollicitée")
	}
	if found.Email != "contact@val.fr" {
		t.Errorf("Artisan() = %+v, attendu l'entrée de Charpentes du Val", found)
	}

	if _, ok := demande.Artisan("Plomberie Générale"); ok {
		t.Error("Artisan() a retrouvé une entreprise qui n'a pas été sollicitée")
	}
}
