package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Romain-Badino/Avanti/internal/document"
)

// DocumentRepo implémente [document.Repository] sur PostgreSQL.
//
// Les colonnes portent le vocabulaire du domaine (nom_fichier, taille,
// categorie, cible_type) : c'est le même modèle vu des deux côtés, et la
// correspondance se lit sans table de traduction. Seul le contenu binaire n'y
// figure pas — il appartient au port de stockage, jamais à la base.
type DocumentRepo struct {
	pool *pgxpool.Pool
}

// NewDocumentRepo construit le dépôt sur un pool de connexions existant. Le
// pool reste la propriété de l'appelant — cmd/avanti, qui l'ouvre et le ferme.
func NewDocumentRepo(pool *pgxpool.Pool) (*DocumentRepo, error) {
	if pool == nil {
		return nil, errors.New("postgres : pool de connexions manquant")
	}
	return &DocumentRepo{pool: pool}, nil
}

// documentColumns est la liste de sélection commune aux lectures. La
// factoriser garantit que les colonnes arrivent dans l'ordre qu'attend
// [scanDocument].
const documentColumns = `id, nom_fichier, mime, taille, categorie, description, ` +
	`cible_type, cible_id, televerse_par, cree_le, modifie_le`

// Create insère une pièce.
func (d *DocumentRepo) Create(ctx context.Context, doc document.Document) error {
	id, err := writeUUID(doc.ID.String(), "document")
	if err != nil {
		return err
	}
	auteur, err := writeUUID(doc.UploadedBy.String(), "acteur")
	if err != nil {
		return err
	}

	const query = `
		INSERT INTO documents (` + documentColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	if _, err := d.pool.Exec(ctx, query,
		id, doc.FileName, doc.MimeType, doc.SizeBytes, doc.Category.String(), doc.Description,
		doc.Target.Type.String(), doc.Target.ID, auteur, doc.CreatedAt, doc.UpdatedAt); err != nil {
		return fmt.Errorf("insertion de la pièce %s : %w", doc.ID, err)
	}

	return nil
}

// ByID lit une pièce par son identifiant.
func (d *DocumentRepo) ByID(ctx context.Context, docID document.ID) (document.Document, error) {
	id, err := lookupUUID(docID.String(), document.ErrUnknownDocument)
	if err != nil {
		return document.Document{}, err
	}

	const query = `SELECT ` + documentColumns + ` FROM documents WHERE id = $1`

	doc, err := scanDocument(d.pool.QueryRow(ctx, query, id))
	if err != nil {
		return document.Document{}, fmt.Errorf("lecture de la pièce %s : %w", docID, err)
	}

	return doc, nil
}

// List renvoie toutes les pièces, de la plus récemment déposée à la plus
// ancienne.
//
// Sans pagination : même raisonnement que pour les demandes de devis — le
// dossier d'une reconstruction compte quelques centaines de pièces au plus, et
// le jour où ce ne serait plus vrai, c'est le port du domaine qu'il faudrait
// revoir, pas seulement cette requête. L'identifiant départage les dépôts
// simultanés, pour un ordre d'affichage stable.
func (d *DocumentRepo) List(ctx context.Context) ([]document.Document, error) {
	const query = `SELECT ` + documentColumns + ` FROM documents ORDER BY cree_le DESC, id`

	documents, err := queryAll(ctx, d.pool, scanDocument, query)
	if err != nil {
		return nil, fmt.Errorf("lecture des pièces : %w", err)
	}

	return documents, nil
}

// ListByTarget renvoie les pièces rattachées à une cible, de la plus récente à
// la plus ancienne. C'est la requête que sert l'index partiel de la migration
// 00008.
func (d *DocumentRepo) ListByTarget(ctx context.Context, target document.Target) ([]document.Document, error) {
	const query = `SELECT ` + documentColumns + ` FROM documents
		WHERE cible_type = $1 AND cible_id = $2 ORDER BY cree_le DESC, id`

	documents, err := queryAll(ctx, d.pool, scanDocument, query, target.Type.String(), target.ID)
	if err != nil {
		return nil, fmt.Errorf("lecture des pièces de la cible %s/%s : %w", target.Type, target.ID, err)
	}

	return documents, nil
}

// ListByTargets renvoie les pièces rattachées à plusieurs cibles du même type,
// groupées par identifiant de cible.
//
// Une seule requête sert tout le lot : l'égalité sur cible_id devient un ANY,
// et c'est le même index documents_par_cible de la migration 00008 qui la
// porte. Le groupement se fait en mémoire, ce qui préserve l'ordre du ORDER BY
// à l'intérieur de chaque groupe.
func (d *DocumentRepo) ListByTargets(ctx context.Context, targetType document.TargetType, ids []string) (map[string][]document.Document, error) {
	const query = `SELECT ` + documentColumns + ` FROM documents
		WHERE cible_type = $1 AND cible_id = ANY($2) ORDER BY cree_le DESC, id`

	documents, err := queryAll(ctx, d.pool, scanDocument, query, targetType.String(), ids)
	if err != nil {
		return nil, fmt.Errorf("lecture des pièces des cibles de type %s : %w", targetType, err)
	}

	grouped := make(map[string][]document.Document, len(ids))
	for _, doc := range documents {
		grouped[doc.Target.ID] = append(grouped[doc.Target.ID], doc)
	}

	return grouped, nil
}

// scanDocument reconstruit une pièce depuis une ligne.
func scanDocument(row rowScanner) (document.Document, error) {
	var (
		doc       document.Document
		id        pgtype.UUID
		categorie string
		cibleType string
		auteur    pgtype.UUID
	)

	err := row.Scan(&id, &doc.FileName, &doc.MimeType, &doc.SizeBytes, &categorie, &doc.Description,
		&cibleType, &doc.Target.ID, &auteur, &doc.CreatedAt, &doc.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return document.Document{}, document.ErrUnknownDocument
	}
	if err != nil {
		return document.Document{}, err
	}

	doc.ID = document.ID(id.String())
	doc.Category = document.Category(categorie)
	doc.Target.Type = document.TargetType(cibleType)
	doc.UploadedBy = document.ActeurID(auteur.String())

	return doc, nil
}
