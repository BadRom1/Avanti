package export_test

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/adapters/export"
	"github.com/Romain-Badino/Avanti/internal/finance"
)

// dossierTest assemble un dossier représentatif : une facture payée et
// remboursée avec pièces jointes, une facture impayée hors devis, un acompte —
// avec des accents partout où le vrai contenu en aura.
func dossierTest() finance.DossierAssurance {
	date := time.Date(2026, time.April, 3, 0, 0, 0, 0, time.UTC)
	paid := time.Date(2026, time.May, 5, 0, 0, 0, 0, time.UTC)
	refunded := time.Date(2026, time.June, 9, 0, 0, 0, 0, time.UTC)

	return finance.DossierAssurance{
		GeneratedAt: time.Date(2026, time.August, 14, 10, 0, 0, 0, time.UTC),
		Intitule:    "Reconstruction — maison de Régny",
		Factures: []finance.LigneFacture{
			{
				DevisLibelle: "Charpente — Charpentes du Val",
				Entreprise:   "Charpentes du Val",
				Numero:       "F-2026-042",
				Date:         date,
				Montant:      1_180_050,
				Paiement:     finance.PaiementPayee,
				PaidAt:       paid,
				Assurance: finance.SuiviAssurance{
					Statut:           finance.AssuranceRemboursee,
					SentAt:           paid,
					MontantRembourse: 1_000_000,
					RefundedAt:       refunded,
				},
				Pieces: []finance.PieceJointe{
					{FileName: "facture-042.pdf", Category: "facture"},
					{FileName: "règlement.pdf", Category: "courrier_assurance"},
				},
			},
			{
				Entreprise: "Négoce Matériaux",
				Date:       date,
				Montant:    40_000,
				Paiement:   finance.PaiementImpayee,
				Assurance:  finance.SuiviAssurance{Statut: finance.AssuranceNonEnvoyee},
			},
		},
		Acomptes: []finance.LigneAcompte{
			{
				DevisLibelle: "Charpente — Charpentes du Val",
				Entreprise:   "Charpentes du Val",
				Date:         date,
				Montant:      500_000,
				Moyen:        finance.MoyenCheque,
				Assurance:    finance.SuiviAssurance{Statut: finance.AssuranceEnvoyee, SentAt: paid},
			},
		},
		Totaux: finance.TotauxDossier{
			Engage:    1_180_050,
			Facture:   1_220_050,
			Paye:      1_680_050,
			Rembourse: 1_000_000,
		},
	}
}

// TestCSVRoundTrip écrit le dossier puis le relit avec encoding/csv : c'est la
// preuve que le fichier est un CSV régulier — même nombre de champs partout —
// et que les valeurs y sont exactement celles attendues.
func TestCSVRoundTrip(t *testing.T) {
	t.Parallel()

	format := export.NewCSV()

	if got := format.ContentType(); !strings.HasPrefix(got, "text/csv") {
		t.Errorf("ContentType() = %q", got)
	}
	if got := format.FileExtension(); got != "csv" {
		t.Errorf("FileExtension() = %q", got)
	}

	var buf bytes.Buffer
	if err := format.Write(&buf, dossierTest()); err != nil {
		t.Fatalf("Write() échoué : %v", err)
	}

	// Le BOM UTF-8 ouvre le fichier : c'est lui qui évite à Excel-Windows de
	// lire les accents en ANSI. Il est retiré avant la relecture, comme le
	// ferait un tableur.
	if !strings.HasPrefix(buf.String(), "\xEF\xBB\xBF") {
		t.Error("le CSV ne commence pas par le BOM UTF-8")
	}
	body := strings.TrimPrefix(buf.String(), "\xEF\xBB\xBF")

	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("le CSV produit ne se relit pas : %v", err)
	}

	// 1 en-tête + 2 factures + 1 acompte + 4 totaux.
	if len(rows) != 8 {
		t.Fatalf("le CSV compte %d lignes, attendu 8", len(rows))
	}

	header := rows[0]
	if header[0] != "Type" || header[5] != "Montant (EUR)" || header[12] != "Pièces jointes" {
		t.Errorf("en-tête = %v", header)
	}

	facture := rows[1]
	switch {
	case facture[0] != "Facture":
		t.Errorf("Type = %q", facture[0])
	case facture[1] != "Charpente — Charpentes du Val":
		t.Errorf("Devis = %q", facture[1])
	case facture[3] != "F-2026-042":
		t.Errorf("Numéro = %q", facture[3])
	case facture[4] != "03/04/2026":
		t.Errorf("Date = %q", facture[4])
	case facture[5] != "11800,50":
		t.Errorf("Montant = %q", facture[5])
	case facture[6] != "Payée" || facture[7] != "05/05/2026":
		t.Errorf("règlement = (%q, %q)", facture[6], facture[7])
	case facture[8] != "Remboursée" || facture[10] != "10000,00" || facture[11] != "09/06/2026":
		t.Errorf("assurance = (%q, %q, %q)", facture[8], facture[10], facture[11])
	case !strings.Contains(facture[12], "facture-042.pdf (facture)"):
		t.Errorf("pièces = %q", facture[12])
	case !strings.Contains(facture[12], "règlement.pdf"):
		t.Errorf("pièces = %q, l'accent s'est perdu", facture[12])
	}

	impayee := rows[2]
	if impayee[1] != "" || impayee[6] != "Impayée" || impayee[7] != "" || impayee[10] != "" {
		t.Errorf("facture impayée hors devis = %v", impayee)
	}

	acompte := rows[3]
	// L'accord suit la nature de la pièce : un acompte est « Envoyé ».
	if acompte[0] != "Acompte" || acompte[5] != "5000,00" || acompte[6] != "Chèque" || acompte[8] != "Envoyé" {
		t.Errorf("acompte = %v", acompte)
	}
	// Pas encore remboursé : la colonne reste vide, pas « 0,00 ».
	if acompte[10] != "" {
		t.Errorf("remboursé = %q, attendu vide", acompte[10])
	}

	totaux := map[string]string{}
	for _, row := range rows[4:] {
		totaux[row[0]] = row[5]
	}
	want := map[string]string{
		"Total engagé":    "11800,50",
		"Total facturé":   "12200,50",
		"Total payé":      "16800,50",
		"Total remboursé": "10000,00",
	}
	for label, montant := range want {
		if totaux[label] != montant {
			t.Errorf("%s = %q, attendu %q", label, totaux[label], montant)
		}
	}
}

// TestCSVEmptyDossier : un chantier sans pièce rend un CSV valide — en-tête et
// totaux à zéro, rien d'autre.
func TestCSVEmptyDossier(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := export.NewCSV().Write(&buf, finance.DossierAssurance{}); err != nil {
		t.Fatalf("Write() échoué : %v", err)
	}

	body := strings.TrimPrefix(buf.String(), "\xEF\xBB\xBF")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("le CSV produit ne se relit pas : %v", err)
	}
	if len(rows) != 5 {
		t.Errorf("le CSV compte %d lignes, attendu en-tête + 4 totaux", len(rows))
	}
	if rows[1][5] != "0,00" {
		t.Errorf("total engagé = %q, attendu 0,00", rows[1][5])
	}
}

// TestCSVNeutralizesFormulas : une cellule de texte libre qui commence comme
// une formule de tableur est désamorcée par une apostrophe — sans quoi une
// entreprise saisie « =SOMME(...) » s'exécuterait chez qui ouvre le fichier.
// Les montants, formatés par l'adapter, ne sont pas touchés.
func TestCSVNeutralizesFormulas(t *testing.T) {
	t.Parallel()

	dossier := finance.DossierAssurance{
		Factures: []finance.LigneFacture{
			{
				DevisLibelle: "+Charpente",
				Entreprise:   "=SOMME(A1:A9)",
				Numero:       "@F-42",
				Date:         time.Date(2026, time.April, 3, 0, 0, 0, 0, time.UTC),
				Montant:      1_180_050,
				Paiement:     finance.PaiementImpayee,
				Assurance:    finance.SuiviAssurance{Statut: finance.AssuranceNonEnvoyee},
				Pieces:       []finance.PieceJointe{{FileName: "-2+3.pdf", Category: "facture"}},
			},
		},
		Acomptes: []finance.LigneAcompte{
			{
				Entreprise: "\tEntreprise Tab",
				Date:       time.Date(2026, time.April, 3, 0, 0, 0, 0, time.UTC),
				Montant:    100,
				Moyen:      finance.MoyenVirement,
				Assurance:  finance.SuiviAssurance{Statut: finance.AssuranceNonEnvoyee},
			},
		},
	}

	var buf bytes.Buffer
	if err := export.NewCSV().Write(&buf, dossier); err != nil {
		t.Fatalf("Write() échoué : %v", err)
	}

	body := strings.TrimPrefix(buf.String(), "\xEF\xBB\xBF")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("le CSV produit ne se relit pas : %v", err)
	}

	facture := rows[1]
	switch {
	case facture[1] != "'+Charpente":
		t.Errorf("devis = %q, attendu la formule neutralisée", facture[1])
	case facture[2] != "'=SOMME(A1:A9)":
		t.Errorf("entreprise = %q, attendu la formule neutralisée", facture[2])
	case facture[3] != "'@F-42":
		t.Errorf("numéro = %q, attendu la formule neutralisée", facture[3])
	case facture[12] != "'-2+3.pdf (facture)":
		t.Errorf("pièces = %q, attendu la formule neutralisée", facture[12])
	case facture[5] != "11800,50":
		t.Errorf("montant = %q — les montants formatés par l'adapter ne se neutralisent pas", facture[5])
	}

	if acompte := rows[2]; acompte[2] != "'\tEntreprise Tab" {
		t.Errorf("entreprise de l'acompte = %q, attendu la tabulation neutralisée", acompte[2])
	}
}
