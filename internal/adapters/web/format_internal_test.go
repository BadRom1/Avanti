package web

import (
	"errors"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/devis"
)

// TestParseMontant est le test qui garde la promesse la plus fragile de
// l'interface : une saisie en euros devient des centimes entiers, exactement.
// Un flottant glissé dans cette conversion se verrait ici, et nulle part
// ailleurs avant le papier de l'artisan.
func TestParseMontant(t *testing.T) {
	t.Parallel()

	cases := map[string]devis.Montant{
		"12500":    1_250_000,
		"12500,00": 1_250_000,
		"12500.00": 1_250_000,
		"11800,50": 1_180_050,
		"11800.50": 1_180_050,
		// Les trois espaces qu'un navigateur ou un copier-coller peut produire :
		// l'espace ordinaire, l'insécable, et l'insécable étroite de l'imprimerie
		// française. Les trois doivent passer.
		"12 500,00":      1_250_000,
		"12\u00a0500,00": 1_250_000,
		"12\u202f500,00": 1_250_000,
		"12 500,00 €":    1_250_000,
		"  11800,50  ":   1_180_050,
		"12,5":           1_250,
		",50":            50,
		"0,01":           1,
		"100000000,00":   devis.MaxMontant,
	}

	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			got, err := parseMontant(raw)
			if err != nil {
				t.Fatalf("parseMontant(%q) = %v", raw, err)
			}
			if got != want {
				t.Errorf("parseMontant(%q) = %d centimes, attendu %d", raw, int64(got), int64(want))
			}
		})
	}
}

func TestParseMontantRejects(t *testing.T) {
	t.Parallel()

	cases := map[string]error{
		"":                errMontantVide,
		"   ":             errMontantVide,
		"€":               errMontantVide,
		"douze mille":     errMontantIllisible,
		"12 500,00,00":    errMontantIllisible,
		"11800,500":       errMontantIllisible,
		"-100":            errMontantIllisible,
		"1e6":             errMontantIllisible,
		",":               errMontantIllisible,
		"1234567890123":   errMontantIllisible,
		"0":               errMontantHorsBornes,
		"0,00":            errMontantHorsBornes,
		"100000000,01":    errMontantHorsBornes,
		"999999999999,99": errMontantHorsBornes,
	}

	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if _, err := parseMontant(raw); !errors.Is(err, want) {
				t.Errorf("parseMontant(%q) = %v, attendu %v", raw, err, want)
			}
		})
	}
}

// TestFormatMontant : le nombre s'écrit à la française, groupé par milliers avec
// une espace insécable — un montant ne se coupe pas en fin de ligne.
func TestFormatMontant(t *testing.T) {
	t.Parallel()

	cases := map[devis.Montant]string{
		0:           "0,00",
		1:           "0,01",
		50:          "0,50",
		100:         "1,00",
		99_999:      "999,99",
		100_000:     "1 000,00",
		1_180_050:   "11 800,50",
		1_250_000:   "12 500,00",
		100_000_000: "1 000 000,00",
		-1_180_050:  "-11 800,50",
	}

	for montant, want := range cases {
		if got := formatMontant(montant); got != want {
			t.Errorf("formatMontant(%d) = %q, attendu %q", int64(montant), got, want)
		}
	}
}

// TestMontantRoundTrip ferme la boucle : ce qui s'affiche se ressaisit à
// l'identique. Sans cela, corriger un devis à partir de ce qu'on lit à l'écran
// changerait sa valeur.
func TestMontantRoundTrip(t *testing.T) {
	t.Parallel()

	for _, montant := range []devis.Montant{1, 99, 100, 12_345, 1_180_050, 1_250_000, devis.MaxMontant} {
		formatted := formatMontant(montant)

		parsed, err := parseMontant(formatted)
		if err != nil {
			t.Fatalf("parseMontant(%q) = %v", formatted, err)
		}
		if parsed != montant {
			t.Errorf("aller-retour de %d centimes : %q rend %d", int64(montant), formatted, int64(parsed))
		}
	}
}

func TestParseDate(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC)

	for _, raw := range []string{"2026-03-12", "12/03/2026", "  2026-03-12  "} {
		got, err := parseDate(raw)
		if err != nil {
			t.Fatalf("parseDate(%q) = %v", raw, err)
		}
		if !got.Equal(want) {
			t.Errorf("parseDate(%q) = %s, attendu %s", raw, got, want)
		}
	}

	for _, raw := range []string{"", "  ", "12 mars 2026", "2026-13-01", "31/02/2026"} {
		if _, err := parseDate(raw); !errors.Is(err, errDateIllisible) {
			t.Errorf("parseDate(%q) = %v, attendu %v", raw, err, errDateIllisible)
		}
	}
}

func TestFormatDate(t *testing.T) {
	t.Parallel()

	instant := time.Date(2026, time.March, 12, 14, 30, 0, 0, time.UTC)

	if got := formatDate(instant); got != "12/03/2026" {
		t.Errorf("formatDate() = %q", got)
	}
	if got := formatDateInput(instant); got != "2026-03-12" {
		t.Errorf("formatDateInput() = %q", got)
	}
	// Une date nulle ne s'affiche pas en « 01/01/0001 », qui n'apprendrait rien.
	if got := formatDate(time.Time{}); got != "" {
		t.Errorf("formatDate(zéro) = %q, attendu vide", got)
	}
	if got := formatDateInput(time.Time{}); got != "" {
		t.Errorf("formatDateInput(zéro) = %q, attendu vide", got)
	}
}

func TestParseValidityDays(t *testing.T) {
	t.Parallel()

	cases := map[string]time.Duration{
		"":   0,
		"  ": 0,
		"0":  0,
		"30": 30 * 24 * time.Hour,
		"90": 90 * 24 * time.Hour,
	}

	for raw, want := range cases {
		got, err := parseValidityDays(raw)
		if err != nil {
			t.Fatalf("parseValidityDays(%q) = %v", raw, err)
		}
		if got != want {
			t.Errorf("parseValidityDays(%q) = %s, attendu %s", raw, got, want)
		}
	}

	for _, raw := range []string{"-1", "trente", "1,5"} {
		if _, err := parseValidityDays(raw); err == nil {
			t.Errorf("parseValidityDays(%q) doit échouer", raw)
		}
	}

	if got := formatValidityDays(30 * 24 * time.Hour); got != "30" {
		t.Errorf("formatValidityDays() = %q, attendu 30", got)
	}
	if got := formatValidityDays(0); got != "" {
		t.Errorf("formatValidityDays(0) = %q, attendu vide", got)
	}
}
