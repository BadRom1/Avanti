package document_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/document"
)

func TestNewServiceRejectsMissingPorts(t *testing.T) {
	t.Parallel()

	if _, err := document.NewService(document.ServiceOptions{Storage: newMemStorage()}); err == nil {
		t.Error("NewService() sans dépôt doit échouer")
	}
	if _, err := document.NewService(document.ServiceOptions{Repo: newMemRepo()}); err == nil {
		t.Error("NewService() sans stockage doit échouer")
	}
}

// TestUploadStoresContentAndMetadata : le chemin heureux écrit le contenu sous
// la clé de l'identifiant et les métadonnées normalisées.
func TestUploadStoresContentAndMetadata(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	doc := f.upload(t, func(in *document.UploadInput) {
		in.FileName = "  Devis-Charpente.PDF "
		in.Category = " DEVIS_SIGNE "
		in.MimeType = " Application/PDF "
		in.Description = "  Devis signé le 12 mars.  "
	})

	if doc.ID != "id-1" {
		t.Errorf("ID = %q, attendu id-1", doc.ID)
	}
	if doc.FileName != "Devis-Charpente.PDF" {
		t.Errorf("FileName = %q", doc.FileName)
	}
	if doc.MimeType != "application/pdf" {
		t.Errorf("MimeType = %q", doc.MimeType)
	}
	if doc.Category != document.CategoryDevisSigne {
		t.Errorf("Category = %q", doc.Category)
	}
	if doc.Description != "Devis signé le 12 mars." {
		t.Errorf("Description = %q", doc.Description)
	}
	if !doc.Target.Zero() {
		t.Errorf("Target = %+v, attendu la valeur zéro", doc.Target)
	}
	if doc.UploadedBy != acteur {
		t.Errorf("UploadedBy = %q", doc.UploadedBy)
	}
	if !doc.CreatedAt.Equal(instantDepot) || !doc.UpdatedAt.Equal(instantDepot) {
		t.Errorf("horodatages = %s / %s", doc.CreatedAt, doc.UpdatedAt)
	}

	if got := string(f.storage.contents["id-1"]); got != "contenu du devis" {
		t.Errorf("contenu stocké = %q", got)
	}
	stored, err := f.repo.ByID(t.Context(), doc.ID)
	if err != nil {
		t.Fatalf("ByID() échoué : %v", err)
	}
	if stored != doc {
		t.Errorf("métadonnées stockées = %+v, attendu %+v", stored, doc)
	}
}

// TestUploadWithTarget : un rattachement valide est normalisé et conservé.
func TestUploadWithTarget(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	doc := f.upload(t, func(in *document.UploadInput) {
		in.Target = document.Target{Type: " DEVIS ", ID: "  6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5  "}
	})

	want := document.Target{Type: document.TargetDevis, ID: "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5"}
	if doc.Target != want {
		t.Errorf("Target = %+v, attendu %+v", doc.Target, want)
	}
}

// TestUploadRejections : chaque invariant refuse, avec l'erreur sentinelle qui
// permet à l'interface de traduire, et rien n'est écrit — ni contenu, ni
// métadonnées.
func TestUploadRejections(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		mutate func(*document.UploadInput)
		want   error
	}{
		"nom de fichier vide": {
			mutate: func(in *document.UploadInput) { in.FileName = "   " },
			want:   document.ErrEmptyFileName,
		},
		"nom de fichier réduit à un chemin": {
			mutate: func(in *document.UploadInput) { in.FileName = "dossier/" },
			want:   document.ErrEmptyFileName,
		},
		"nom de fichier « .. »": {
			mutate: func(in *document.UploadInput) { in.FileName = ".." },
			want:   document.ErrEmptyFileName,
		},
		"nom de fichier trop long": {
			mutate: func(in *document.UploadInput) { in.FileName = strings.Repeat("a", 256) + ".pdf" },
			want:   document.ErrFileNameTooLong,
		},
		"type de contenu interdit": {
			mutate: func(in *document.UploadInput) { in.MimeType = "image/svg+xml" },
			want:   document.ErrUnsupportedMimeType,
		},
		"type de contenu avec paramètre": {
			mutate: func(in *document.UploadInput) { in.MimeType = "application/pdf; charset=binary" },
			want:   document.ErrUnsupportedMimeType,
		},
		"catégorie inconnue": {
			mutate: func(in *document.UploadInput) { in.Category = "selfie" },
			want:   document.ErrUnknownCategory,
		},
		"description trop longue": {
			mutate: func(in *document.UploadInput) { in.Description = strings.Repeat("a", 2001) },
			want:   document.ErrDescriptionTooLong,
		},
		"taille nulle": {
			mutate: func(in *document.UploadInput) { in.SizeBytes = 0 },
			want:   document.ErrEmptyContent,
		},
		"taille négative": {
			mutate: func(in *document.UploadInput) { in.SizeBytes = -1 },
			want:   document.ErrEmptyContent,
		},
		"fichier trop volumineux": {
			mutate: func(in *document.UploadInput) { in.SizeBytes = document.MaxFileSize + 1 },
			want:   document.ErrFileTooLarge,
		},
		"contenu absent": {
			mutate: func(in *document.UploadInput) { in.Content = nil },
			want:   document.ErrEmptyContent,
		},
		"cible sans identifiant": {
			mutate: func(in *document.UploadInput) { in.Target = document.Target{Type: "devis"} },
			want:   document.ErrInvalidTarget,
		},
		"identifiant sans type de cible": {
			mutate: func(in *document.UploadInput) { in.Target = document.Target{ID: "abc"} },
			want:   document.ErrInvalidTarget,
		},
		"type de cible inconnu": {
			mutate: func(in *document.UploadInput) { in.Target = document.Target{Type: "chantier", ID: "abc"} },
			want:   document.ErrInvalidTarget,
		},
		"acteur manquant": {
			mutate: func(in *document.UploadInput) { in.By = "" },
			want:   document.ErrMissingActor,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			in := validInput()
			tc.mutate(&in)

			if _, err := f.service.Upload(t.Context(), in); !errors.Is(err, tc.want) {
				t.Fatalf("Upload() = %v, attendu %v", err, tc.want)
			}
			if len(f.storage.contents) != 0 {
				t.Error("un contenu a été écrit malgré le refus")
			}
			if len(f.repo.documents) != 0 {
				t.Error("des métadonnées ont été écrites malgré le refus")
			}
		})
	}
}

// TestUploadFileSizeAtBound : la borne est inclusive — un fichier d'exactement
// 25 Mio passe.
func TestUploadFileSizeAtBound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	doc := f.upload(t, func(in *document.UploadInput) {
		in.SizeBytes = document.MaxFileSize
		in.Content = bytes.NewReader(bytes.Repeat([]byte{'a'}, int(document.MaxFileSize)))
	})

	if doc.SizeBytes != document.MaxFileSize {
		t.Errorf("SizeBytes = %d", doc.SizeBytes)
	}
}

// TestUploadRejectsSizeMismatch : les octets réellement transmis doivent être
// exactement ceux annoncés — dans les deux sens — et le contenu déjà écrit est
// nettoyé. C'est un bug d'adapter, pas une faute de saisie : l'erreur n'a pas
// de message de formulaire.
func TestUploadRejectsSizeMismatch(t *testing.T) {
	t.Parallel()

	cases := map[string]int64{
		// Le contenu fait len(contenuTest) octets ; l'annonce ment des deux côtés.
		"contenu plus long que l'annonce":  int64(len(contenuTest)) - 4,
		"contenu plus court que l'annonce": int64(len(contenuTest)) + 4,
	}

	for name, declared := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			in := validInput()
			in.SizeBytes = declared

			if _, err := f.service.Upload(t.Context(), in); !errors.Is(err, document.ErrContentSizeMismatch) {
				t.Fatalf("Upload() = %v, attendu ErrContentSizeMismatch", err)
			}
			if len(f.storage.contents) != 0 {
				t.Error("le contenu en désaccord n'a pas été nettoyé")
			}
			if len(f.repo.documents) != 0 {
				t.Error("des métadonnées ont été écrites malgré le désaccord")
			}
		})
	}
}

// TestUploadEmptyFileBeatsMimeCheck : un fichier vide sniffe en text/plain —
// deux refus possibles, et c'est « fichier vide » qui doit gagner : c'est la
// cause, l'autre n'est que son symptôme.
func TestUploadEmptyFileBeatsMimeCheck(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	in := validInput()
	in.SizeBytes = 0
	in.MimeType = "text/plain"
	in.Content = bytes.NewReader(nil)

	if _, err := f.service.Upload(t.Context(), in); !errors.Is(err, document.ErrEmptyContent) {
		t.Fatalf("Upload() = %v, attendu ErrEmptyContent avant ErrUnsupportedMimeType", err)
	}
}

// TestUploadCleansOrphanWhenCreateFails : si les métadonnées ne s'écrivent
// pas, le contenu déjà déposé est supprimé — c'est la promesse « pas
// d'orphelin » du service, en meilleur effort.
func TestUploadCleansOrphanWhenCreateFails(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	panne := errors.New("la base est tombée")
	f.repo.failOn("Create", panne)

	_, err := f.service.Upload(t.Context(), validInput())
	if !errors.Is(err, panne) {
		t.Fatalf("Upload() = %v, attendu la panne du dépôt", err)
	}
	if len(f.storage.contents) != 0 {
		t.Error("le contenu orphelin n'a pas été supprimé")
	}
	if len(f.storage.deleted) != 1 || f.storage.deleted[0] != "id-1" {
		t.Errorf("suppressions = %v, attendu [id-1]", f.storage.deleted)
	}
}

// TestUploadKeepsRepoErrorWhenCleanupFails : la panne du nettoyage de secours
// ne masque pas celle qui compte, celle des métadonnées.
func TestUploadKeepsRepoErrorWhenCleanupFails(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	panne := errors.New("la base est tombée")
	f.repo.failOn("Create", panne)
	f.storage.failOn("Delete", errors.New("le stockage aussi"))

	if _, err := f.service.Upload(t.Context(), validInput()); !errors.Is(err, panne) {
		t.Fatalf("Upload() = %v, attendu la panne du dépôt", err)
	}
}

// TestUploadPropagatesStorageFailure : une panne du stockage remonte telle
// quelle, et rien n'est écrit dans le dépôt.
func TestUploadPropagatesStorageFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	panne := errors.New("disque plein")
	f.storage.failOn("Save", panne)

	if _, err := f.service.Upload(t.Context(), validInput()); !errors.Is(err, panne) {
		t.Fatalf("Upload() = %v, attendu la panne du stockage", err)
	}
	if len(f.repo.documents) != 0 {
		t.Error("des métadonnées ont été écrites sans contenu")
	}
}

// TestUploadPropagatesIDFailure : un crypto/rand en panne refuse le dépôt.
func TestUploadPropagatesIDFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	panne := errors.New("plus d'aléa")

	service, err := document.NewService(document.ServiceOptions{
		Repo:    f.repo,
		Storage: f.storage,
		NewID:   func() (document.ID, error) { return "", panne },
	})
	if err != nil {
		t.Fatalf("NewService() échoué : %v", err)
	}

	if _, err := service.Upload(t.Context(), validInput()); !errors.Is(err, panne) {
		t.Fatalf("Upload() = %v, attendu la panne du générateur", err)
	}
}

// TestDocumentsListsNewestFirst passe par le dépôt tel que le port le promet.
func TestDocumentsListsNewestFirst(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.upload(t, func(in *document.UploadInput) { in.FileName = "premier.pdf" })
	f.upload(t, func(in *document.UploadInput) {
		in.FileName = "second.pdf"
		withContent(in, "second contenu")
	})

	documents, err := f.service.Documents(t.Context())
	if err != nil {
		t.Fatalf("Documents() échoué : %v", err)
	}
	if len(documents) != 2 || documents[0].FileName != "second.pdf" {
		t.Errorf("Documents() = %+v, attendu le plus récent d'abord", documents)
	}
}

// TestDocumentReadsByID et l'erreur typée sur l'inconnu.
func TestDocumentReadsByID(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	doc := f.upload(t, nil)

	got, err := f.service.Document(t.Context(), doc.ID)
	if err != nil || got.ID != doc.ID {
		t.Fatalf("Document() = %+v, %v", got, err)
	}

	if _, err := f.service.Document(t.Context(), "id-inconnu"); !errors.Is(err, document.ErrUnknownDocument) {
		t.Errorf("Document(inconnu) = %v, attendu ErrUnknownDocument", err)
	}
}

// TestDocumentsByTarget filtre par cible, et refuse la cible vide ou invalide.
func TestDocumentsByTarget(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	cible := document.Target{Type: document.TargetDevis, ID: "devis-42"}
	f.upload(t, func(in *document.UploadInput) { in.Target = cible })
	f.upload(t, func(in *document.UploadInput) {
		in.FileName = "libre.pdf"
		withContent(in, "pièce libre")
	})

	attaches, err := f.service.DocumentsByTarget(t.Context(), cible)
	if err != nil {
		t.Fatalf("DocumentsByTarget() échoué : %v", err)
	}
	if len(attaches) != 1 || attaches[0].Target != cible {
		t.Errorf("DocumentsByTarget() = %+v", attaches)
	}

	if _, err := f.service.DocumentsByTarget(t.Context(), document.Target{}); !errors.Is(err, document.ErrInvalidTarget) {
		t.Errorf("DocumentsByTarget(zéro) = %v, attendu ErrInvalidTarget", err)
	}
	if _, err := f.service.DocumentsByTarget(t.Context(), document.Target{Type: "chantier", ID: "x"}); !errors.Is(err, document.ErrInvalidTarget) {
		t.Errorf("DocumentsByTarget(type inconnu) = %v, attendu ErrInvalidTarget", err)
	}
}

// TestOpenReturnsMetadataAndContent : Open rend les deux, dans cet ordre —
// jamais de contenu sans métadonnées.
func TestOpenReturnsMetadataAndContent(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	doc := f.upload(t, nil)

	got, content, err := f.service.Open(t.Context(), doc.ID)
	if err != nil {
		t.Fatalf("Open() échoué : %v", err)
	}
	defer func() {
		if closeErr := content.Close(); closeErr != nil {
			t.Errorf("fermeture du contenu : %v", closeErr)
		}
	}()

	if got.ID != doc.ID || got.FileName != doc.FileName {
		t.Errorf("Open() métadonnées = %+v", got)
	}
	raw, err := io.ReadAll(content)
	if err != nil {
		t.Fatalf("lecture du contenu : %v", err)
	}
	if string(raw) != "contenu du devis" {
		t.Errorf("contenu = %q", raw)
	}
}

// TestOpenUnknownDocument : une pièce inconnue rend l'erreur typée sans
// toucher au stockage.
func TestOpenUnknownDocument(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.storage.failOn("Open", errors.New("le stockage ne doit pas être appelé"))

	if _, _, err := f.service.Open(t.Context(), "id-inconnu"); !errors.Is(err, document.ErrUnknownDocument) {
		t.Fatalf("Open(inconnu) = %v, attendu ErrUnknownDocument", err)
	}
}

// TestOpenPropagatesMissingContent : des métadonnées sans contenu sont une
// incohérence — l'erreur du stockage remonte, typée, pour être traitée en
// panne et non en 404.
func TestOpenPropagatesMissingContent(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	doc := f.upload(t, nil)
	delete(f.storage.contents, doc.ID.String())

	if _, _, err := f.service.Open(t.Context(), doc.ID); !errors.Is(err, document.ErrContentNotFound) {
		t.Fatalf("Open() = %v, attendu ErrContentNotFound", err)
	}
}
