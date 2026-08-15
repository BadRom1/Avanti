package web_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"

	"github.com/Romain-Badino/Avanti/internal/document"
)

// memDocumentRepo est un [document.Repository] en mémoire pour les tests de
// l'adapter web. Il tient les promesses du port — lecture vide typée, listing
// de la plus récente à la plus ancienne — parce que les gestionnaires HTTP en
// dépendent.
//
// Le verrou n'est pas décoratif : le gestionnaire sous test est exercé par
// plusieurs requêtes, et `go test -race` le remarquerait.
type memDocumentRepo struct {
	mu        sync.Mutex
	documents map[document.ID]document.Document
	order     []document.ID
}

func newMemDocumentRepo() *memDocumentRepo {
	return &memDocumentRepo{documents: make(map[document.ID]document.Document)}
}

func (r *memDocumentRepo) Create(_ context.Context, doc document.Document) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.documents[doc.ID] = doc
	r.order = append(r.order, doc.ID)

	return nil
}

func (r *memDocumentRepo) ByID(_ context.Context, id document.ID) (document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	doc, ok := r.documents[id]
	if !ok {
		return document.Document{}, document.ErrUnknownDocument
	}

	return doc, nil
}

func (r *memDocumentRepo) List(_ context.Context) ([]document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	documents := make([]document.Document, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		documents = append(documents, r.documents[r.order[i]])
	}

	return documents, nil
}

func (r *memDocumentRepo) ListByTarget(_ context.Context, target document.Target) ([]document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var documents []document.Document
	for i := len(r.order) - 1; i >= 0; i-- {
		if doc := r.documents[r.order[i]]; doc.Target == target {
			documents = append(documents, doc)
		}
	}

	return documents, nil
}

func (r *memDocumentRepo) ListByTargets(_ context.Context, targetType document.TargetType, ids []string) (map[string][]document.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

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

// documentParNom retrouve une pièce par son nom de fichier, pour que les
// tests désignent « devis-charpente.pdf » plutôt qu'un UUID tiré au hasard.
func (r *memDocumentRepo) documentParNom(nom string) (document.Document, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, id := range r.order {
		if strings.EqualFold(r.documents[id].FileName, nom) {
			return r.documents[id], true
		}
	}

	return document.Document{}, false
}

// memDocumentStorage est un [document.Storage] en mémoire.
//
// Un fake plutôt que l'adapter filesystem : depguard interdit à une famille
// d'adapters d'en importer une autre, tests compris (R4) — et le contrat du
// port, que ce fake tient (clé occupée refusée, clé absente typée, suppression
// idempotente), est tout ce dont les gestionnaires HTTP dépendent.
type memDocumentStorage struct {
	mu       sync.Mutex
	contents map[string][]byte
}

func newMemDocumentStorage() *memDocumentStorage {
	return &memDocumentStorage{contents: make(map[string][]byte)}
}

func (s *memDocumentStorage) Save(_ context.Context, key string, content io.Reader) error {
	raw, err := io.ReadAll(content)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.contents[key]; exists {
		return document.ErrContentAlreadyExists
	}
	s.contents[key] = raw

	return nil
}

func (s *memDocumentStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, ok := s.contents[key]
	if !ok {
		return nil, document.ErrContentNotFound
	}

	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (s *memDocumentStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.contents, key)

	return nil
}
