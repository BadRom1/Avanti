package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Repository est le port de persistance des métadonnées.
//
// Les implémentations sont attendues sur un point que le domaine ne peut pas
// vérifier lui-même : rendre [ErrUnknownDocument] (éventuellement enveloppée)
// quand une lecture ne trouve rien. Tout le reste des erreurs remonte tel quel
// et sera traité comme une panne.
type Repository interface {
	// Create insère une pièce.
	Create(ctx context.Context, doc Document) error
	// ByID lit une pièce par son identifiant.
	ByID(ctx context.Context, id ID) (Document, error)
	// List renvoie toutes les pièces, de la plus récemment déposée à la plus
	// ancienne.
	List(ctx context.Context) ([]Document, error)
	// ListByTarget renvoie les pièces rattachées à une cible, de la plus
	// récemment déposée à la plus ancienne. Une cible sans pièce rend une liste
	// vide, pas une erreur.
	ListByTarget(ctx context.Context, target Target) ([]Document, error)
}

// Storage est le port du contenu binaire — et le point d'extension officiel
// du domaine (docs/ARCHITECTURE.md §3) : remplacer le disque local par un
// objet compatible S3 consiste à implémenter ces trois méthodes et à le dire à
// la configuration.
//
// La clé est l'identifiant de la pièce, un UUID tiré par crypto/rand : elle
// n'est pas devinable, et un stockage n'a jamais à en inventer une.
//
// Le contrat, que le domaine ne peut pas vérifier lui-même :
//
//   - [Storage.Save] refuse une clé déjà occupée, avec
//     [ErrContentAlreadyExists] (éventuellement enveloppée). Les clés sont
//     tirées au hasard : retrouver la sienne déjà prise est un bug ou une
//     tentative de réécriture, et écraser en silence serait pire que
//     d'échouer ;
//   - [Storage.Open] rend [ErrContentNotFound] (éventuellement enveloppée)
//     quand la clé ne désigne rien ;
//   - [Storage.Delete] est idempotent : supprimer une clé absente n'est pas
//     une erreur. C'est ce qui rend le nettoyage de secours de
//     [Service.Upload] rejouable sans précaution.
type Storage interface {
	// Save écrit le contenu sous la clé donnée.
	Save(ctx context.Context, key string, content io.Reader) error
	// Open ouvre le contenu de la clé donnée. Le lecteur rendu est à refermer
	// par l'appelant.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete supprime le contenu de la clé donnée.
	Delete(ctx context.Context, key string) error
}

// Service porte les cas d'usage du domaine.
//
// Il ne journalise pas et ne lit aucune variable d'environnement : ce qu'il
// lui faut arrive par [ServiceOptions], conformément à R1 de
// docs/ARCHITECTURE.md.
//
// V1 assumée sans suppression ni re-rattachement : la politique de rétention
// des pièces est une décision encore ouverte (docs/ARCHITECTURE.md §8), et un
// cas d'usage d'effacement écrit avant elle la préjugerait.
type Service struct {
	repo    Repository
	storage Storage
	clock   func() time.Time
	newID   func() (ID, error)
}

// ServiceOptions rassemble les dépendances du service.
type ServiceOptions struct {
	// Repo est le port de persistance des métadonnées. Obligatoire.
	Repo Repository
	// Storage est le port du contenu binaire. Obligatoire.
	Storage Storage
	// Clock donne l'heure courante. Nil signifie time.Now.
	Clock func() time.Time
	// NewID tire un identifiant. Nil signifie [NewID].
	NewID func() (ID, error)
}

// NewService construit le service.
func NewService(opts ServiceOptions) (*Service, error) {
	switch {
	case opts.Repo == nil:
		return nil, errors.New("document : dépôt de métadonnées manquant")
	case opts.Storage == nil:
		return nil, errors.New("document : stockage de contenu manquant")
	}

	service := &Service{
		repo:    opts.Repo,
		storage: opts.Storage,
		clock:   opts.Clock,
		newID:   opts.NewID,
	}
	if service.clock == nil {
		service.clock = time.Now
	}
	if service.newID == nil {
		service.newID = NewID
	}

	return service, nil
}

// UploadInput est ce qu'il faut fournir pour déposer une pièce.
type UploadInput struct {
	// FileName est le nom de fichier d'origine. Obligatoire ; il sera nettoyé.
	FileName string
	// MimeType est le type de contenu constaté par l'appelant — pas celui que
	// le client annonce. Doit figurer dans [AllowedMimeTypes].
	MimeType string
	// SizeBytes est la taille réelle du contenu, en octets, telle que
	// l'appelant l'a constatée. Strictement positive, bornée par [MaxFileSize].
	SizeBytes int64
	// Content est le contenu binaire, lu une seule fois. Il traverse le
	// service sans y être retenu : le domaine le confie au [Storage] tel quel.
	Content io.Reader
	// Category classe la pièce. Obligatoire.
	Category string
	// Description précise la pièce. Facultative.
	Description string
	// Target rattache la pièce à ce qu'elle justifie. La valeur zéro vaut
	// « pièce libre ».
	Target Target
	// By est l'acteur qui dépose. Obligatoire.
	By ActeurID
}

// Upload valide les métadonnées, écrit le contenu puis les métadonnées, et
// renvoie ce qui a été stocké.
//
// L'ordre des deux écritures est un choix : le contenu part vers le [Storage]
// d'abord, les métadonnées ensuite. Si l'écriture des métadonnées échoue, le
// contenu est supprimé en meilleur effort ; s'il survit malgré tout, c'est un
// fichier orphelin sous une clé que rien ne référence — un déchet, pas une
// incohérence. L'ordre inverse produirait le contraire : une pièce listée dont
// le téléchargement échoue, c'est-à-dire une promesse cassée visible de
// l'utilisateur.
func (s *Service) Upload(ctx context.Context, in UploadInput) (Document, error) {
	doc, err := s.buildDocument(in)
	if err != nil {
		return Document{}, err
	}

	if in.Content == nil {
		return Document{}, fmt.Errorf("%w : aucun contenu fourni", ErrEmptyContent)
	}

	// Le flux est compté au passage, et borné à un octet au-delà de l'annonce :
	// c'est ce qui permet de constater « trop long » sans lire un contenu
	// entier que personne n'a promis. La vérification se fait après le Save —
	// c'est la lecture par le stockage qui fait avancer le compteur.
	content := &countingReader{reader: io.LimitReader(in.Content, doc.SizeBytes+1)}

	if err := s.storage.Save(ctx, doc.ID.String(), content); err != nil {
		return Document{}, fmt.Errorf("écriture du contenu de la pièce %s : %w", doc.ID, err)
	}

	// La taille annoncée a été validée puis stockée dans les métadonnées : si
	// les octets réellement transmis diffèrent, les métadonnées mentiraient —
	// Content-Length faux au téléchargement, contenu tronqué ou gonflé. Le
	// contenu déjà écrit est supprimé (meilleur effort), et l'erreur est
	// [ErrContentSizeMismatch] : un bug de l'adapter appelant, pas une faute
	// utilisateur.
	if content.count != doc.SizeBytes {
		_ = s.storage.Delete(ctx, doc.ID.String()) //nolint:errcheck // meilleur effort : l'écart de taille prime, l'orphelin éventuel est inerte.
		return Document{}, fmt.Errorf("%w : %d octets annoncés, %d transmis", ErrContentSizeMismatch, doc.SizeBytes, content.count)
	}

	if err := s.repo.Create(ctx, doc); err != nil {
		// Nettoyage de secours, en meilleur effort : l'erreur qui compte pour
		// l'appelant est celle des métadonnées, et un contenu orphelin qui
		// survivrait à ce Delete ne référence rien ni n'est référencé.
		_ = s.storage.Delete(ctx, doc.ID.String()) //nolint:errcheck // meilleur effort : l'erreur d'origine prime, l'orphelin éventuel est inerte.
		return Document{}, err
	}

	return doc, nil
}

// countingReader compte les octets qui le traversent.
type countingReader struct {
	reader io.Reader
	count  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.count += int64(n)
	return n, err
}

// buildDocument valide et assemble la pièce. Séparer la construction de
// l'écriture garde chacune lisible, et rend la validation testable sans
// stockage.
//
// La taille est vérifiée avant le type de contenu, et l'ordre est visible de
// l'utilisateur : un fichier vide sniffe en text/plain, et lui répondre « type
// interdit » masquerait la vraie cause — c'est « fichier vide » qu'il doit
// lire.
func (s *Service) buildDocument(in UploadInput) (Document, error) {
	fileName, err := NormalizeFileName(in.FileName)
	if err != nil {
		return Document{}, err
	}
	if sizeErr := checkSize(in.SizeBytes); sizeErr != nil {
		return Document{}, sizeErr
	}
	mimeType, err := normalizeMimeType(in.MimeType)
	if err != nil {
		return Document{}, err
	}
	category, err := NormalizeCategory(in.Category)
	if err != nil {
		return Document{}, err
	}
	description, err := normalizeDescription(in.Description)
	if err != nil {
		return Document{}, err
	}
	target, err := NormalizeTarget(in.Target)
	if err != nil {
		return Document{}, err
	}
	if in.By == "" {
		return Document{}, ErrMissingActor
	}

	id, err := s.newID()
	if err != nil {
		return Document{}, err
	}

	now := s.clock().UTC()

	return Document{
		ID:          id,
		FileName:    fileName,
		MimeType:    mimeType,
		SizeBytes:   in.SizeBytes,
		Category:    category,
		Description: description,
		Target:      target,
		UploadedBy:  in.By,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// checkSize vérifie la taille annoncée du contenu.
func checkSize(size int64) error {
	switch {
	case size <= 0:
		return fmt.Errorf("%w : taille de %d octet(s)", ErrEmptyContent, size)
	case size > MaxFileSize:
		return fmt.Errorf("%w : %d octets, %d au maximum", ErrFileTooLarge, size, MaxFileSize)
	default:
		return nil
	}
}

// Documents renvoie toutes les pièces, de la plus récente à la plus ancienne.
func (s *Service) Documents(ctx context.Context) ([]Document, error) {
	return s.repo.List(ctx)
}

// Document lit une pièce par son identifiant.
func (s *Service) Document(ctx context.Context, id ID) (Document, error) {
	return s.repo.ByID(ctx, id)
}

// DocumentsByTarget renvoie les pièces rattachées à une cible.
//
// Une cible zéro est refusée : « les pièces libres » est une autre question
// que « les pièces de ce devis », et la poser par accident — un identifiant
// vide qui traîne — rendrait une liste qui n'a rien à voir avec ce que
// l'appelant croyait demander.
func (s *Service) DocumentsByTarget(ctx context.Context, target Target) ([]Document, error) {
	normalized, err := NormalizeTarget(target)
	if err != nil {
		return nil, err
	}
	if normalized.Zero() {
		return nil, fmt.Errorf("%w : cible vide", ErrInvalidTarget)
	}

	return s.repo.ListByTarget(ctx, normalized)
}

// Open relit les métadonnées d'une pièce puis ouvre son contenu. Le lecteur
// rendu est à refermer par l'appelant.
//
// L'ordre est une règle, pas une commodité : jamais de contenu servi sans ses
// métadonnées. Ce sont elles qui portent le type et le nom sous lesquels le
// contenu doit être présenté, et c'est la lecture des métadonnées qui décide
// qu'une pièce existe — un contenu ouvert directement contournerait les deux.
func (s *Service) Open(ctx context.Context, id ID) (Document, io.ReadCloser, error) {
	doc, err := s.repo.ByID(ctx, id)
	if err != nil {
		return Document{}, nil, err
	}

	content, err := s.storage.Open(ctx, doc.ID.String())
	if err != nil {
		return Document{}, nil, fmt.Errorf("ouverture du contenu de la pièce %s : %w", doc.ID, err)
	}

	return doc, content, nil
}
