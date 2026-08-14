package finance_test

import (
	"testing"

	"github.com/Romain-Badino/Avanti/internal/finance"
)

func TestMontantValid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		montant finance.Montant
		want    bool
	}{
		{name: "un centime", montant: 1, want: true},
		{name: "montant courant", montant: 1_180_050, want: true},
		{name: "borne haute exacte", montant: finance.MaxMontant, want: true},
		{name: "zéro", montant: 0, want: false},
		{name: "négatif", montant: -1, want: false},
		{name: "au-delà de la borne", montant: finance.MaxMontant + 1, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.montant.Valid(); got != tc.want {
				t.Errorf("Valid(%d) = %v, attendu %v", int64(tc.montant), got, tc.want)
			}
		})
	}
}

func TestMontantSplit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		montant  finance.Montant
		euros    int64
		centimes int64
	}{
		{name: "montant avec centimes", montant: 1_180_050, euros: 11_800, centimes: 50},
		{name: "montant rond", montant: 500_000, euros: 5_000, centimes: 0},
		{name: "centimes seuls", montant: 99, euros: 0, centimes: 99},
		{name: "négatif rendu positif", montant: -1_050, euros: 10, centimes: 50},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			euros, centimes := tc.montant.Split()
			if euros != tc.euros || centimes != tc.centimes {
				t.Errorf("Split() = (%d, %d), attendu (%d, %d)", euros, centimes, tc.euros, tc.centimes)
			}
		})
	}
}

func TestMontantString(t *testing.T) {
	t.Parallel()

	if got := finance.Montant(1_180_050).String(); got != "1180050 centimes" {
		t.Errorf("String() = %q", got)
	}
}

func TestNewIDShape(t *testing.T) {
	t.Parallel()

	id, err := finance.NewID()
	if err != nil {
		t.Fatalf("NewID() échoué : %v", err)
	}
	if len(id.String()) != 36 || id.String()[14] != '4' {
		t.Errorf("NewID() = %q, attendu un UUID v4 canonique", id)
	}

	again, err := finance.NewID()
	if err != nil {
		t.Fatalf("NewID() échoué : %v", err)
	}
	if id == again {
		t.Error("deux identifiants tirés à la suite sont identiques")
	}
}
