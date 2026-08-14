package export_test

import (
	"bytes"
	"compress/zlib"
	"io"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/adapters/export"
	"github.com/Romain-Badino/Avanti/internal/finance"
)

// TestPDFRendersDossier : le rendu est un vrai PDF — l'entête %PDF- fait foi —
// et son texte porte les montants, les libellés et les accents attendus.
func TestPDFRendersDossier(t *testing.T) {
	t.Parallel()

	format := export.NewPDF()

	if got := format.ContentType(); got != "application/pdf" {
		t.Errorf("ContentType() = %q", got)
	}
	if got := format.FileExtension(); got != "pdf" {
		t.Errorf("FileExtension() = %q", got)
	}

	var buf bytes.Buffer
	if err := format.Write(&buf, dossierTest()); err != nil {
		t.Fatalf("Write() échoué : %v", err)
	}

	raw := buf.Bytes()
	if !bytes.HasPrefix(raw, []byte("%PDF-")) {
		t.Fatalf("le rendu ne commence pas par %%PDF- : %q", raw[:min(len(raw), 16)])
	}

	// Les flux de contenu sont compressés en flate : le texte se lit en les
	// dégonflant. Les chaînes y sont en cp1252 — les accents s'y cherchent
	// dans cet encodage, c'est précisément ce que le test vérifie.
	text := extractPDFStreams(t, raw)

	for _, wanted := range []string{
		"11800,50 EUR", // montant de la facture
		"5000,00 EUR",  // montant de l'acompte
		"10000,00 EUR", // montant remboursé
		"F-2026-042",   // numéro de facture
		"facture-042.pdf",
	} {
		if !strings.Contains(text, wanted) {
			t.Errorf("le texte du PDF ne contient pas %q", wanted)
		}
	}

	// Les accents réels, encodés en cp1252 par le traducteur : « é » = 0xE9,
	// « è » = 0xE8. Un traducteur oublié laisserait l'UTF-8 brut (0xC3 0xA9) et
	// le lecteur afficherait « Ã© ».
	for _, wanted := range []string{
		"G\xe9n\xe9r\xe9 le", // « Généré le »
		"Pay\xe9e",           // statut de paiement « Payée »
		"Rembours\xe9e",      // statut assurance « Remboursée »
		"Ch\xe8que",          // moyen de paiement « Chèque »
		"R\xe9gny",           // l'intitulé du chantier
	} {
		if !strings.Contains(text, wanted) {
			t.Errorf("le texte du PDF ne contient pas %q en cp1252", wanted)
		}
	}
	if strings.Contains(text, "\xc3\xa9") {
		t.Error("le texte du PDF contient de l'UTF-8 brut : le traducteur cp1252 n'a pas été appliqué")
	}
}

// TestPDFEmptyDossier : un chantier sans pièce rend un document valide, avec
// ses sections vides annoncées.
func TestPDFEmptyDossier(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := export.NewPDF().Write(&buf, finance.DossierAssurance{}); err != nil {
		t.Fatalf("Write() échoué : %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Fatal("le rendu ne commence pas par %PDF-")
	}

	text := extractPDFStreams(t, buf.Bytes())
	if !strings.Contains(text, "Aucune pi\xe8ce.") {
		t.Error("les sections vides ne sont pas annoncées")
	}
}

// extractPDFStreams dégonfle tous les flux flate du document et rend leur
// concaténation. C'est une extraction brute — assez pour chercher des chaînes,
// pas pour reconstruire la mise en page.
func extractPDFStreams(t *testing.T, raw []byte) string {
	t.Helper()

	var text strings.Builder
	rest := raw

	for {
		start := bytes.Index(rest, []byte("stream\n"))
		if start < 0 {
			break
		}
		rest = rest[start+len("stream\n"):]

		end := bytes.Index(rest, []byte("endstream"))
		if end < 0 {
			break
		}
		payload := bytes.TrimSuffix(rest[:end], []byte("\n"))
		rest = rest[end:]

		reader, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			// Un flux non compressé (polices, métadonnées) se lit tel quel.
			text.Write(payload)
			continue
		}
		inflated, err := io.ReadAll(reader)
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("fermeture du flux dégonflé : %v", closeErr)
		}
		if err != nil {
			t.Fatalf("dégonflage d'un flux du PDF : %v", err)
		}
		text.Write(inflated)
	}

	if text.Len() == 0 {
		t.Fatal("aucun flux trouvé dans le PDF : l'extraction est cassée")
	}

	return text.String()
}
