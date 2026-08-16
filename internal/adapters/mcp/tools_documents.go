package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/document"
	"github.com/Romain-Badino/Avanti/internal/identity"
)

// documentJSON est une pièce du dossier réduite à ses métadonnées.
//
// Le contenu binaire ne passe JAMAIS par MCP en V1 — décision de cadrage : une
// pièce se télécharge par l'interface web, authentifié par session. Ce tool
// donne à l'agent de quoi savoir ce qui existe et ce que chaque pièce justifie,
// pas de quoi l'ouvrir.
type documentJSON struct {
	ID           string `json:"id" jsonschema:"identifiant de la pièce"`
	NomFichier   string `json:"nom_fichier" jsonschema:"nom de fichier d'origine"`
	TypeMime     string `json:"type_mime" jsonschema:"type de contenu constaté au dépôt"`
	TailleOctets int64  `json:"taille_octets" jsonschema:"taille du contenu, en octets"`
	Categorie    string `json:"categorie" jsonschema:"classement : devis_signe, facture, photo_chantier, rapport_expertise, courrier_assurance ou autre"`
	Description  string `json:"description,omitempty"`
	CibleType    string `json:"cible_type,omitempty" jsonschema:"nature de ce que la pièce justifie : devis, facture ou etape ; vide pour une pièce libre"`
	CibleID      string `json:"cible_id,omitempty" jsonschema:"identifiant de la cible, vide pour une pièce libre"`
	DeposeeLe    string `json:"deposee_le" jsonschema:"date du dépôt, AAAA-MM-JJ"`
}

// documentsListeResult est la sortie de documents_liste.
type documentsListeResult struct {
	Documents []documentJSON `json:"documents" jsonschema:"métadonnées de toutes les pièces, de la plus récente à la plus ancienne — jamais leur contenu"`
}

func (h *Handler) handleDocumentsListe(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, documentsListeResult, error) {
	documents, err := readList(ctx, h, req, "documents_liste", identity.ScopeDocumentRead,
		h.documents.Documents, newDocumentJSON)
	if err != nil {
		return nil, documentsListeResult{}, err
	}

	return nil, documentsListeResult{Documents: documents}, nil
}

func newDocumentJSON(doc document.Document) documentJSON {
	return documentJSON{
		ID:           doc.ID.String(),
		NomFichier:   doc.FileName,
		TypeMime:     doc.MimeType,
		TailleOctets: doc.SizeBytes,
		Categorie:    doc.Category.String(),
		Description:  doc.Description,
		CibleType:    doc.Target.Type.String(),
		CibleID:      doc.Target.ID,
		DeposeeLe:    formatInstant(doc.CreatedAt),
	}
}
