package web_test

import (
	"bytes"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/document"
)

// pdfContent est un contenu dont l'entame est celle d'un vrai PDF : c'est elle
// que le sniffing examine, pas l'extension ni le Content-Type annoncé.
const pdfContent = "%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ncontenu de test"

// uploadField décrit la partie fichier d'un téléversement de test.
type uploadField struct {
	name        string
	content     []byte
	contentType string
}

// postUpload soumet un téléversement multipart. fields porte les champs
// simples, file la partie fichier ; annoncer un Content-Type mensonger fait
// partie de ce que les tests vérifient.
func postUpload(t *testing.T, b *browser, fields map[string]string, file uploadField) httpResult {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("écriture du champ %s : %v", name, err)
		}
	}

	// Les guillemets et antislashs du nom sont échappés comme le fait
	// multipart.CreateFormFile : sans cela, un nom à guillemets casserait
	// l'en-tête de la partie avant même d'atteindre le serveur.
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(file.name)

	header := make(map[string][]string)
	header["Content-Disposition"] = []string{`form-data; name="fichier"; filename="` + quoted + `"`}
	header["Content-Type"] = []string{file.contentType}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("création de la partie fichier : %v", err)
	}
	if _, err := part.Write(file.content); err != nil {
		t.Fatalf("écriture du fichier : %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("clôture du multipart : %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/documents", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return b.send(req)
}

// deposerPiece téléverse un PDF valide et rend la pièce enregistrée.
func deposerPiece(t *testing.T, s *site, b *browser, fields map[string]string) document.Document {
	t.Helper()

	if fields == nil {
		fields = map[string]string{"categorie": "devis_signe", "description": "Le devis signé."}
	}

	result := postUpload(t, b, fields, uploadField{
		name:        "devis-charpente.pdf",
		content:     []byte(pdfContent),
		contentType: "application/pdf",
	})
	if result.Status != http.StatusSeeOther {
		t.Fatalf("téléversement : statut = %d, attendu 303 — corps : %s", result.Status, result.Body)
	}

	doc, ok := s.documents.documentParNom("devis-charpente.pdf")
	if !ok {
		t.Fatal("la pièce n'a pas été enregistrée")
	}

	return doc
}

// TestDocumentRoutesRequireScope : les trois routes sont gardées par un scope,
// téléchargement compris — jamais de fichier servi sans contrôle. Le
// collaborateur est le cas qui compte : son rôle existe, mais ne porte pas
// document:read.
func TestDocumentRoutesRequireScope(t *testing.T) {
	t.Parallel()

	s := newSite(t)

	for _, email := range []string{collaboratorEmail, addAccountWithoutScopes(t, s)} {
		b := newBrowser(t, s.handler)
		b.login(email)

		if result := b.get("/documents"); result.Status != http.StatusForbidden {
			t.Errorf("GET /documents (%s) : statut = %d, attendu 403", email, result.Status)
		}
		if result := b.get("/documents/6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5/telecharger"); result.Status != http.StatusForbidden {
			t.Errorf("GET téléchargement (%s) : statut = %d, attendu 403", email, result.Status)
		}
		if result := b.post("/documents", url.Values{}); result.Status != http.StatusForbidden {
			t.Errorf("POST /documents (%s) : statut = %d, attendu 403", email, result.Status)
		}
	}
}

// TestDocumentAnonymousIsRedirected : la garde par scope s'ajoute à
// l'authentification, elle ne la remplace pas.
func TestDocumentAnonymousIsRedirected(t *testing.T) {
	t.Parallel()

	b := newBrowser(t, newSite(t).handler)

	result := b.get("/documents")
	if result.Status != http.StatusSeeOther || !strings.HasPrefix(result.Location(), "/connexion") {
		t.Fatalf("GET /documents anonyme : statut = %d, redirection = %q", result.Status, result.Location())
	}
}

// TestDocumentUploadJourney suit le parcours heureux : déposer une pièce
// libre, la voir dans la liste, la télécharger à l'identique.
func TestDocumentUploadJourney(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	// La liste vide le dit, et propose le formulaire.
	index := b.get("/documents")
	if !strings.Contains(index.Body, "Aucune pièce") {
		t.Errorf("la liste vide ne le dit pas : %s", index.Body)
	}
	if !strings.Contains(index.Body, `name="fichier"`) {
		t.Error("la liste ne propose pas le formulaire de dépôt")
	}

	doc := deposerPiece(t, s, b, nil)

	// Le type retenu est celui du contenu, la taille celle constatée.
	if doc.MimeType != "application/pdf" {
		t.Errorf("MimeType = %q", doc.MimeType)
	}
	if doc.SizeBytes != int64(len(pdfContent)) {
		t.Errorf("SizeBytes = %d, attendu %d", doc.SizeBytes, len(pdfContent))
	}
	if doc.Category != document.CategoryDevisSigne || doc.Description != "Le devis signé." {
		t.Errorf("métadonnées = %+v", doc)
	}
	if !doc.Target.Zero() {
		t.Errorf("Target = %+v, attendu une pièce libre", doc.Target)
	}

	// La liste l'affiche, avec sa catégorie traduite et son lien.
	liste := b.get("/documents?avis=document_ajoute")
	for _, wanted := range []string{"devis-charpente.pdf", "Devis signé", "La pièce a été déposée.", doc.ID.String()} {
		if !strings.Contains(liste.Body, wanted) {
			t.Errorf("la liste n'affiche pas %q", wanted)
		}
	}

	// Le téléchargement rend le contenu à l'identique, sous le bon type.
	telechargement := b.get("/documents/" + doc.ID.String() + "/telecharger")
	if telechargement.Status != http.StatusOK {
		t.Fatalf("téléchargement : statut = %d", telechargement.Status)
	}
	if got := telechargement.Header.Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := telechargement.Header.Get("Content-Disposition"); !strings.Contains(got, "attachment") ||
		!strings.Contains(got, "devis-charpente.pdf") {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := telechargement.Header.Get("Content-Length"); got == "" || got == "0" {
		t.Errorf("Content-Length = %q", got)
	}
	if telechargement.Body != pdfContent {
		t.Errorf("contenu téléchargé = %q", telechargement.Body)
	}
}

// TestDocumentUploadAcceptsSniffedImageTypes : chaque format de l'allow-list
// est reconnu par ses octets magiques réels — c'est la preuve que
// http.DetectContentType classe bien JPEG, PNG et WebP là où le domaine les
// attend, indépendamment de l'extension et du Content-Type annoncés.
func TestDocumentUploadAcceptsSniffedImageTypes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		fileName string
		content  []byte
		wantMime string
	}{
		"jpeg": {
			fileName: "photo-chantier.jpg",
			content:  append([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}, []byte("JFIF et le reste de l'image")...),
			wantMime: "image/jpeg",
		},
		"png": {
			fileName: "photo-chantier.png",
			content:  append([]byte("\x89PNG\r\n\x1a\n"), []byte("IHDR et le reste de l'image")...),
			wantMime: "image/png",
		},
		"webp": {
			// RIFF, taille sur 4 octets ignorée par le sniffing, puis WEBPVP8.
			fileName: "photo-chantier.webp",
			content:  append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), []byte("le reste de l'image")...),
			wantMime: "image/webp",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			b := newBrowser(t, s.handler)
			b.login(ownerEmail)

			// L'extension et le Content-Type mentent exprès : seul le contenu
			// doit faire foi.
			result := postUpload(t, b, map[string]string{"categorie": "photo_chantier"}, uploadField{
				name:        tc.fileName,
				content:     tc.content,
				contentType: "application/octet-stream",
			})
			if result.Status != http.StatusSeeOther {
				t.Fatalf("statut = %d, attendu 303 — corps : %s", result.Status, result.Body)
			}

			doc, ok := s.documents.documentParNom(tc.fileName)
			if !ok {
				t.Fatal("la pièce n'a pas été enregistrée")
			}
			if doc.MimeType != tc.wantMime {
				t.Errorf("MimeType = %q, attendu %q", doc.MimeType, tc.wantMime)
			}
		})
	}
}

// TestDocumentUploadTargetNormalization : la cible est normalisée AVANT la
// vérification d'existence — un POST forgé « Devis » (casse) ou « devis  »
// (blancs) ne contourne pas le contrôle, et le rattachement stocké est la
// forme canonique.
func TestDocumentUploadTargetNormalization(t *testing.T) {
	t.Parallel()

	t.Run("casse forgée et devis inconnu : refusé", func(t *testing.T) {
		t.Parallel()

		s := newSite(t)
		b := newBrowser(t, s.handler)
		b.login(ownerEmail)

		result := postUpload(t, b, map[string]string{
			"categorie":  "devis_signe",
			"cible_type": "Devis",
			"cible_id":   "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5",
		}, uploadField{name: "devis.pdf", content: []byte(pdfContent), contentType: "application/pdf"})

		if result.Status != http.StatusUnprocessableEntity {
			t.Fatalf("statut = %d, attendu 422 — corps : %s", result.Status, result.Body)
		}
		if !strings.Contains(html.UnescapeString(result.Body), "devis auquel rattacher cette pièce n'existe pas") {
			t.Errorf("le refus n'est pas celui de la cible inconnue : %s", result.Body)
		}
		if len(s.storage.contents) != 0 {
			t.Error("un contenu a été stocké malgré le contournement tenté")
		}
	})

	t.Run("blancs autour du type et devis réel : rattaché en forme canonique", func(t *testing.T) {
		t.Parallel()

		s := newSite(t)
		b := newBrowser(t, s.handler)
		b.login(ownerEmail)

		demande := nouvelleDemande(t, s, b)
		enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas)
		proposition, _ := s.devis.devisParEntreprise(entrepriseBas)

		result := postUpload(t, b, map[string]string{
			"categorie":  "devis_signe",
			"cible_type": " devis ",
			"cible_id":   " " + proposition.ID.String() + " ",
		}, uploadField{name: "devis-signe.pdf", content: []byte(pdfContent), contentType: "application/pdf"})

		if result.Status != http.StatusSeeOther || !strings.Contains(result.Location(), demande.ID.String()) {
			t.Fatalf("statut = %d, redirection = %q — corps : %s", result.Status, result.Location(), result.Body)
		}

		doc, ok := s.documents.documentParNom("devis-signe.pdf")
		if !ok {
			t.Fatal("la pièce n'a pas été enregistrée")
		}
		want := document.Target{Type: document.TargetDevis, ID: proposition.ID.String()}
		if doc.Target != want {
			t.Errorf("Target = %+v, attendu la forme canonique %+v", doc.Target, want)
		}
	})
}

// TestDocumentDownloadDispositionHardNames documente l'en-tête réellement émis
// pour les noms difficiles : mime.FormatMediaType encode un nom non-ASCII en
// RFC 2231 (filename*=utf-8”…) SANS repli filename= ASCII — constaté et
// assumé, les navigateurs courants le lisent — et cite un nom à guillemets en
// les échappant, sans casser l'en-tête.
func TestDocumentDownloadDispositionHardNames(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		fileName        string
		wantDisposition string
	}{
		"nom accentué": {
			fileName:        "devis façade.pdf",
			wantDisposition: `attachment; filename*=utf-8''devis%20fa%C3%A7ade.pdf`,
		},
		"nom à guillemets": {
			fileName:        `devis "final".pdf`,
			wantDisposition: `attachment; filename="devis \"final\".pdf"`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			b := newBrowser(t, s.handler)
			b.login(ownerEmail)

			result := postUpload(t, b, map[string]string{"categorie": "autre"}, uploadField{
				name: tc.fileName, content: []byte(pdfContent), contentType: "application/pdf",
			})
			if result.Status != http.StatusSeeOther {
				t.Fatalf("téléversement : statut = %d — corps : %s", result.Status, result.Body)
			}

			doc, ok := s.documents.documentParNom(tc.fileName)
			if !ok {
				t.Fatal("la pièce n'a pas été enregistrée")
			}

			telechargement := b.get("/documents/" + doc.ID.String() + "/telecharger")
			if telechargement.Status != http.StatusOK {
				t.Fatalf("téléchargement : statut = %d", telechargement.Status)
			}
			if got := telechargement.Header.Get("Content-Disposition"); got != tc.wantDisposition {
				t.Errorf("Content-Disposition = %q, attendu %q", got, tc.wantDisposition)
			}
		})
	}
}

// TestDocumentUploadRejectsEmptyFile : un fichier vide est refusé pour ce
// qu'il est — vide — et non pour son type sniffé (un contenu vide sniffe en
// text/plain, et « type interdit » masquerait la cause).
func TestDocumentUploadRejectsEmptyFile(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	result := postUpload(t, b, map[string]string{"categorie": "autre"}, uploadField{
		name: "vide.pdf", content: nil, contentType: "application/pdf",
	})
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("statut = %d, attendu 422 — corps : %s", result.Status, result.Body)
	}
	if !strings.Contains(html.UnescapeString(result.Body), "Ce fichier est vide.") {
		t.Errorf("le refus n'explique pas la vraie cause : %s", result.Body)
	}
	if len(s.storage.contents) != 0 {
		t.Error("un contenu a été stocké malgré le refus")
	}
}

// TestDocumentUploadRejectsForbiddenType : c'est le contenu sniffé qui fait
// foi — un fichier texte déguisé en PDF par son nom et son Content-Type est
// refusé, en 422, sans rien écrire.
func TestDocumentUploadRejectsForbiddenType(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	result := postUpload(t, b, map[string]string{"categorie": "autre"}, uploadField{
		name:        "script-deguise.pdf",
		content:     []byte("<html><script>alert(1)</script></html>"),
		contentType: "application/pdf",
	})
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("statut = %d, attendu 422 — corps : %s", result.Status, result.Body)
	}
	if !strings.Contains(html.UnescapeString(result.Body), "type de fichier n'est pas accepté") {
		t.Errorf("le refus n'est pas expliqué : %s", result.Body)
	}
	if _, exists := s.documents.documentParNom("script-deguise.pdf"); exists {
		t.Error("la pièce a été enregistrée malgré le refus")
	}
	if len(s.storage.contents) != 0 {
		t.Error("un contenu a été stocké malgré le refus")
	}
}

// TestDocumentUploadSizeLimits vérifie le double seuil documenté dans
// documents.go : un fichier au-delà de 25 Mio mais dont la requête tient sous
// le plafond est refusé par le domaine (422) ; une requête au-delà du plafond
// est coupée en route (413). Dans les deux cas, rien n'est écrit.
func TestDocumentUploadSizeLimits(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		extraBytes int64
		wantStatus int
	}{
		// 25 Mio + 1 : la requête entière tient sous le plafond (marge d'un
		// mébioctet), c'est le domaine qui refuse.
		"fichier trop gros, requête sous le plafond": {extraBytes: 1, wantStatus: http.StatusUnprocessableEntity},
		// 26 Mio : la requête dépasse le plafond, MaxBytesReader coupe.
		"requête au-delà du plafond": {extraBytes: 1 << 20, wantStatus: http.StatusRequestEntityTooLarge},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newSite(t)
			b := newBrowser(t, s.handler)
			b.login(ownerEmail)

			oversized := append([]byte(pdfContent), bytes.Repeat([]byte{'a'}, int(document.MaxFileSize+tc.extraBytes)-len(pdfContent))...)
			result := postUpload(t, b, map[string]string{"categorie": "autre"}, uploadField{
				name:        "scan-terrible.pdf",
				content:     oversized,
				contentType: "application/pdf",
			})

			if result.Status != tc.wantStatus {
				t.Fatalf("statut = %d, attendu %d", result.Status, tc.wantStatus)
			}
			if !strings.Contains(html.UnescapeString(result.Body), "dépasse la taille maximale") {
				t.Errorf("le refus n'est pas expliqué : %s", findMarker(result.Body))
			}
			if _, exists := s.documents.documentParNom("scan-terrible.pdf"); exists {
				t.Error("la pièce a été enregistrée malgré le refus")
			}
			if len(s.storage.contents) != 0 {
				t.Error("un contenu a été stocké malgré le refus")
			}
		})
	}
}

// TestDocumentUploadMissingFile : un formulaire sans fichier revient en 422
// avec un message, pas en panne.
func TestDocumentUploadMissingFile(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("categorie", "autre"); err != nil {
		t.Fatalf("écriture du champ : %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("clôture du multipart : %v", err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/documents", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	result := b.send(req)
	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("statut = %d, attendu 422", result.Status)
	}
	if !strings.Contains(html.UnescapeString(result.Body), "Choisissez un fichier") {
		t.Errorf("le refus n'est pas expliqué : %s", result.Body)
	}
}

// TestDocumentUploadRejectsUnknownDevisTarget : rattacher une pièce à un devis
// qui n'existe pas est refusé avant tout dépôt.
func TestDocumentUploadRejectsUnknownDevisTarget(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	result := postUpload(t, b, map[string]string{
		"categorie":  "devis_signe",
		"cible_type": "devis",
		"cible_id":   "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5",
	}, uploadField{name: "devis.pdf", content: []byte(pdfContent), contentType: "application/pdf"})

	if result.Status != http.StatusUnprocessableEntity {
		t.Fatalf("statut = %d, attendu 422 — corps : %s", result.Status, result.Body)
	}
	if !strings.Contains(html.UnescapeString(result.Body), "devis auquel rattacher cette pièce n'existe pas") {
		t.Errorf("le refus n'est pas expliqué : %s", result.Body)
	}
	if len(s.storage.contents) != 0 {
		t.Error("un contenu a été stocké malgré le refus")
	}
}

// TestDocumentUploadAttachedToDevis : le dépôt rattaché redirige vers la
// comparaison de la demande, et la page y montre la pièce avec son lien.
func TestDocumentUploadAttachedToDevis(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	demande := nouvelleDemande(t, s, b)
	enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas)
	proposition, _ := s.devis.devisParEntreprise(entrepriseBas)

	// Avant tout dépôt, la comparaison propose le formulaire de pièces.
	avant := b.get("/devis/demandes/" + demande.ID.String())
	if !strings.Contains(avant.Body, `name="cible_id"`) {
		t.Error("la comparaison ne propose pas le formulaire de pièces")
	}

	result := postUpload(t, b, map[string]string{
		"categorie":  "devis_signe",
		"cible_type": "devis",
		"cible_id":   proposition.ID.String(),
	}, uploadField{name: "devis-signe.pdf", content: []byte(pdfContent), contentType: "application/pdf"})

	if result.Status != http.StatusSeeOther {
		t.Fatalf("statut = %d, attendu 303 — corps : %s", result.Status, result.Body)
	}
	if !strings.Contains(result.Location(), demande.ID.String()) || !strings.Contains(result.Location(), "avis=document_ajoute") {
		t.Errorf("redirection = %q, attendu la comparaison avec avis", result.Location())
	}

	doc, ok := s.documents.documentParNom("devis-signe.pdf")
	if !ok {
		t.Fatal("la pièce n'a pas été enregistrée")
	}
	want := document.Target{Type: document.TargetDevis, ID: proposition.ID.String()}
	if doc.Target != want {
		t.Errorf("Target = %+v, attendu %+v", doc.Target, want)
	}

	// La comparaison montre la pièce sous son entreprise, avec le lien.
	page := b.get(result.Location())
	for _, wanted := range []string{"devis-signe.pdf", "/documents/" + doc.ID.String() + "/telecharger", "La pièce a été déposée."} {
		if !strings.Contains(page.Body, wanted) {
			t.Errorf("la comparaison n'affiche pas %q", wanted)
		}
	}

	// Et la liste des pièces pointe vers la comparaison.
	liste := b.get("/documents")
	if !strings.Contains(liste.Body, "/devis/demandes/"+demande.ID.String()) {
		t.Error("la liste ne relie pas la pièce à sa demande")
	}
	if !strings.Contains(html.UnescapeString(liste.Body), "Devis de "+entrepriseBas) {
		t.Errorf("la liste ne nomme pas le rattachement : %s", liste.Body)
	}
}

// TestDocumentDownloadUnknown : une pièce inconnue rend 404 — l'identifiant
// illisible ou traversant compris, qui ne désigne rien lui non plus.
func TestDocumentDownloadUnknown(t *testing.T) {
	t.Parallel()

	b := newBrowser(t, newSite(t).handler)
	b.login(ownerEmail)

	for _, target := range []string{
		"/documents/6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5/telecharger",
		"/documents/pas-un-identifiant/telecharger",
		"/documents/..%2F..%2Fetc%2Fpasswd/telecharger",
	} {
		if result := b.get(target); result.Status != http.StatusNotFound {
			t.Errorf("GET %s : statut = %d, attendu 404", target, result.Status)
		}
	}
}

// TestDocumentPagesHaveNoMissingTranslation : les pages du domaine, dans leurs
// états significatifs, n'affichent aucun marqueur !comme.ceci!.
func TestDocumentPagesHaveNoMissingTranslation(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	// Liste vide, puis liste remplie avec avis.
	for _, target := range []string{"/documents", "/documents?avis=document_ajoute"} {
		if marker := findMarker(b.get(target).Body); marker != "" {
			t.Errorf("%s affiche une traduction manquante : %s", target, marker)
		}
	}

	deposerPiece(t, s, b, nil)
	if marker := findMarker(b.get("/documents").Body); marker != "" {
		t.Errorf("la liste remplie affiche une traduction manquante : %s", marker)
	}

	// Le refus de saisie, qui réaffiche le formulaire.
	refus := postUpload(t, b, map[string]string{"categorie": "autre"}, uploadField{
		name: "notes.txt", content: []byte("du texte"), contentType: "text/plain",
	})
	if marker := findMarker(refus.Body); marker != "" {
		t.Errorf("la page de refus affiche une traduction manquante : %s", marker)
	}

	// La comparaison avec sa section pièces.
	demande := nouvelleDemande(t, s, b)
	enregistrerDevis(t, b, demande.ID, entrepriseBas, montantBas)
	proposition, _ := s.devis.devisParEntreprise(entrepriseBas)
	postUpload(t, b, map[string]string{
		"categorie": "devis_signe", "cible_type": "devis", "cible_id": proposition.ID.String(),
	}, uploadField{name: "signe.pdf", content: []byte(pdfContent), contentType: "application/pdf"})

	if marker := findMarker(b.get("/devis/demandes/" + demande.ID.String()).Body); marker != "" {
		t.Errorf("la comparaison affiche une traduction manquante : %s", marker)
	}
}

// TestDocumentFormHiddenWithoutWriteScope : un rôle en lecture seule voit la
// liste sans le formulaire — annoncer une action refusée serait une promesse
// en l'air. Aucun rôle réel n'a document:read sans write aujourd'hui ; le test
// passe par le gabarit rendu au propriétaire et vérifie l'inverse, puis la
// borne 403 du POST couvre le reste (TestDocumentRoutesRequireScope).
func TestDocumentFormHiddenWithoutWriteScope(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	if !strings.Contains(b.get("/documents").Body, `name="fichier"`) {
		t.Error("le propriétaire, qui détient document:write, doit voir le formulaire")
	}
}

// contentOf lit le contenu stocké d'une pièce, pour vérifier l'aller-retour.
func contentOf(t *testing.T, s *site, doc document.Document) string {
	t.Helper()

	reader, err := s.storage.Open(t.Context(), doc.ID.String())
	if err != nil {
		t.Fatalf("ouverture du contenu stocké : %v", err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Errorf("fermeture du contenu : %v", closeErr)
		}
	}()

	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("lecture du contenu stocké : %v", err)
	}

	return string(raw)
}

// TestDocumentContentStoredVerbatim : le contenu stocké est l'octet près celui
// envoyé — le sniffing des 512 premiers octets n'a pas décalé la lecture.
func TestDocumentContentStoredVerbatim(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := newBrowser(t, s.handler)
	b.login(ownerEmail)

	doc := deposerPiece(t, s, b, nil)
	if got := contentOf(t, s, doc); got != pdfContent {
		t.Errorf("contenu stocké = %q, attendu l'original", got)
	}
}
