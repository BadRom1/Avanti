// Harnais des tests du domaine document.
//
// Le dépôt et le stockage en mémoire ci-dessous ne sont pas des commodités :
// ils tiennent les mêmes promesses que celles que [document.Repository] et
// [document.Storage] exigent d'une implémentation réelle — erreurs de lecture
// typées, clé déjà occupée refusée, suppression idempotente. Un fake plus
// permissif laisserait passer des tests que PostgreSQL ou le disque feraient
// échouer.
package document_test

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/document"
)

// instantDepot est le repère temporel des tests. Une date fixe plutôt que
// time.Now : une suite qui dépend de l'heure d'exécution finit par échouer une
// nuit de changement d'heure, et jamais sur le poste de qui l'a écrite.
var instantDepot = time.Date(2026, time.April, 3, 11, 0, 0, 0, time.UTC)

// acteur est l'identifiant d'acteur employé par défaut : une valeur, jamais un
// compte — le domaine ne sait pas la résoudre et n'a pas à le savoir.
const acteur document.ActeurID = "9f1c2f6e-2b4a-4d3c-9f6a-1c2d3e4f5a6b"

// memRepo est un [document.Repository] en mémoire.
type memRepo struct {
	documents map[document.ID]document.Document
	order     []document.ID

	// failures fait échouer une méthode nommée, pour vérifier que le service
	// propage une panne du dépôt au lieu de la déguiser en refus métier.
	failures map[string]error
}

func newMemRepo() *memRepo {
	return &memRepo{
		documents: make(map[document.ID]document.Document),
		failures:  make(map[string]error),
	}
}

func (r *memRepo) failOn(method string, err error) {
	r.failures[method] = err
}

func (r *memRepo) Create(_ context.Context, doc document.Document) error {
	if err := r.failures["Create"]; err != nil {
		return err
	}

	r.documents[doc.ID] = doc
	r.order = append(r.order, doc.ID)

	return nil
}

func (r *memRepo) ByID(_ context.Context, id document.ID) (document.Document, error) {
	if err := r.failures["ByID"]; err != nil {
		return document.Document{}, err
	}

	doc, ok := r.documents[id]
	if !ok {
		return document.Document{}, document.ErrUnknownDocument
	}

	return doc, nil
}

// List rend les pièces de la plus récente à la plus ancienne, comme le fait la
// requête SQL.
func (r *memRepo) List(_ context.Context) ([]document.Document, error) {
	if err := r.failures["List"]; err != nil {
		return nil, err
	}

	documents := make([]document.Document, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		documents = append(documents, r.documents[r.order[i]])
	}

	return documents, nil
}

func (r *memRepo) ListByTarget(_ context.Context, target document.Target) ([]document.Document, error) {
	if err := r.failures["ListByTarget"]; err != nil {
		return nil, err
	}

	var documents []document.Document
	for i := len(r.order) - 1; i >= 0; i-- {
		if doc := r.documents[r.order[i]]; doc.Target == target {
			documents = append(documents, doc)
		}
	}

	return documents, nil
}

func (r *memRepo) ListByTargets(_ context.Context, targetType document.TargetType, ids []string) (map[string][]document.Document, error) {
	if err := r.failures["ListByTargets"]; err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}

	grouped := make(map[string][]document.Document, len(ids))
	for i := len(r.order) - 1; i >= 0; i-- {
		doc := r.documents[r.order[i]]
		if doc.Target.Type == targetType && wanted[doc.Target.ID] {
			grouped[doc.Target.ID] = append(grouped[doc.Target.ID], doc)
		}
	}

	return grouped, nil
}

// memStorage est un [document.Storage] en mémoire. Il tient le contrat du
// port : Save refuse une clé occupée, Open rend l'erreur du domaine sur une
// clé absente, Delete est idempotent.
type memStorage struct {
	contents map[string][]byte
	deleted  []string

	failures map[string]error
}

func newMemStorage() *memStorage {
	return &memStorage{
		contents: make(map[string][]byte),
		failures: make(map[string]error),
	}
}

func (s *memStorage) failOn(method string, err error) {
	s.failures[method] = err
}

func (s *memStorage) Save(_ context.Context, key string, content io.Reader) error {
	if err := s.failures["Save"]; err != nil {
		return err
	}

	if _, exists := s.contents[key]; exists {
		return document.ErrContentAlreadyExists
	}

	raw, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	s.contents[key] = raw

	return nil
}

func (s *memStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	if err := s.failures["Open"]; err != nil {
		return nil, err
	}

	raw, ok := s.contents[key]
	if !ok {
		return nil, document.ErrContentNotFound
	}

	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (s *memStorage) Delete(_ context.Context, key string) error {
	if err := s.failures["Delete"]; err != nil {
		return err
	}

	s.deleted = append(s.deleted, key)
	delete(s.contents, key)

	return nil
}

// fixture monte un service sur des ports neufs, avec une horloge arrêtée et
// des identifiants prévisibles.
type fixture struct {
	service *document.Service
	repo    *memRepo
	storage *memStorage
	now     time.Time
	ids     int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{repo: newMemRepo(), storage: newMemStorage(), now: instantDepot}

	service, err := document.NewService(document.ServiceOptions{
		Repo:    f.repo,
		Storage: f.storage,
		Clock:   func() time.Time { return f.now },
		NewID: func() (document.ID, error) {
			f.ids++
			return document.ID("id-" + strconv.Itoa(f.ids)), nil
		},
	})
	if err != nil {
		t.Fatalf("document.NewService() échoué : %v", err)
	}
	f.service = service

	return f
}

// contenuTest est le contenu déposé par défaut. La taille annoncée doit être
// exactement la sienne : le service vérifie l'accord entre l'annonce et les
// octets transmis.
const contenuTest = "contenu du devis"

// validInput rend une entrée de dépôt valide et complète. Les champs qu'un
// test veut particuliers, il les écrase après coup — en gardant taille et
// contenu d'accord, sauf à vouloir tester leur désaccord.
func validInput() document.UploadInput {
	return document.UploadInput{
		FileName:    "devis-charpente.pdf",
		MimeType:    "application/pdf",
		SizeBytes:   int64(len(contenuTest)),
		Content:     bytes.NewReader([]byte(contenuTest)),
		Category:    "devis_signe",
		Description: "Devis signé le 12 mars.",
		By:          acteur,
	}
}

// withContent règle contenu et taille annoncée d'un même geste.
func withContent(in *document.UploadInput, content string) {
	in.Content = bytes.NewReader([]byte(content))
	in.SizeBytes = int64(len(content))
}

// upload dépose une pièce valide et rend ce qui a été stocké.
func (f *fixture) upload(t *testing.T, mutate func(*document.UploadInput)) document.Document {
	t.Helper()

	in := validInput()
	if mutate != nil {
		mutate(&in)
	}

	doc, err := f.service.Upload(t.Context(), in)
	if err != nil {
		t.Fatalf("Upload() échoué : %v", err)
	}

	return doc
}
