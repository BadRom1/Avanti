package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Romain-Badino/Avanti/internal/document"
)

// S3 implémente [document.Storage] sur un objet compatible S3 (MinIO, Garage,
// AWS…). C'est le second adapter de stockage, celui qui démontre le modèle
// d'extension de docs/ARCHITECTURE.md §3 : le port du domaine est le point
// d'extension, et brancher un stockage objet consiste à implémenter ses trois
// méthodes puis à le choisir dans la configuration — sans toucher au domaine.
type S3 struct {
	client *minio.Client
	bucket string
}

// S3Options rassemble ce qu'il faut pour joindre le service S3.
type S3Options struct {
	// Endpoint est l'adresse du service, au format hôte ou hôte:port, sans
	// schéma — le schéma est décidé par UseSSL. Obligatoire.
	Endpoint string
	// Bucket est le seau qui reçoit les contenus. Il doit exister ; l'adapter
	// ne le crée pas — c'est une opération d'administration, pas de service.
	Bucket string
	// AccessKey et SecretKey sont les identifiants d'accès. Obligatoires.
	AccessKey string
	SecretKey string
	// Region est la région du seau. Facultative : la plupart des S3
	// auto-hébergés n'en ont qu'une et l'ignorent.
	Region string
	// UseSSL commande le passage en HTTPS. À ne désactiver qu'en
	// développement, contre un MinIO local.
	UseSSL bool
}

// NewS3 construit le stockage. Aucun échange réseau n'a lieu ici : une
// configuration erronée se manifeste au premier accès, pas à la construction —
// le démarrage d'Avanti ne dépend ainsi pas de la disponibilité du S3.
func NewS3(opts S3Options) (*S3, error) {
	switch {
	case opts.Endpoint == "":
		return nil, errors.New("storage : adresse du service S3 manquante")
	case opts.Bucket == "":
		return nil, errors.New("storage : nom du seau S3 manquant")
	case opts.AccessKey == "" || opts.SecretKey == "":
		return nil, errors.New("storage : identifiants d'accès S3 manquants")
	}

	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure: opts.UseSSL,
		Region: opts.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage : client S3 sur %s : %w", opts.Endpoint, err)
	}

	return &S3{client: client, bucket: opts.Bucket}, nil
}

// Save écrit le contenu sous la clé donnée, et refuse une clé déjà occupée.
//
// Le refus passe par un StatObject préalable : S3 n'offre pas d'écriture
// conditionnelle universelle, et le contrat du port — Save refuse une clé
// occupée — vaut d'être tenu même avec une fenêtre de course résiduelle. Les
// clés sont des UUID tirés par crypto/rand : deux écritures simultanées de la
// même clé supposent déjà un bug en amont, et la vérification attrape le cas
// qui compte, le rejeu. La fenêtre a un pire cas connu : si deux Upload
// passaient le Stat avec la même clé, le second PUT écraserait le premier,
// puis l'échec du second Create déclencherait un nettoyage qui supprime le
// contenu que les premières métadonnées référencent — des métadonnées sans
// contenu, l'inverse de l'orphelin inerte. Assumé : cela suppose une collision
// d'UUID v4, dont la probabilité est négligeable.
//
// Le contenu est bufferisé en mémoire avant l'envoi, pour connaître sa taille
// exacte et partir en une seule requête PUT. C'est un choix assumé, borné par
// le domaine : [document.MaxFileSize] vaut 25 Mio, et un flux qui dépasse
// cette borne est refusé ici plutôt qu'envoyé — le service a validé une taille
// annoncée, le stockage vérifie la taille réelle.
func (s *S3) Save(ctx context.Context, key string, content io.Reader) error {
	if err := checkKey(key); err != nil {
		return err
	}

	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err == nil {
		return fmt.Errorf("%w : clé %s", document.ErrContentAlreadyExists, key)
	} else if !isNoSuchKey(err) {
		return fmt.Errorf("storage : vérification de la clé %s : %w", key, err)
	}

	raw, err := io.ReadAll(io.LimitReader(content, document.MaxFileSize+1))
	if err != nil {
		return fmt.Errorf("storage : lecture du contenu de %s : %w", key, err)
	}
	if int64(len(raw)) > document.MaxFileSize {
		return fmt.Errorf("%w : le flux dépasse %d octets", document.ErrFileTooLarge, document.MaxFileSize)
	}

	// DisableMultipart : un contenu borné à 25 Mio part en une seule requête,
	// le découpage multi-parties n'apporterait que des états intermédiaires à
	// nettoyer. DisableContentSha256 : sans elle, un client hors TLS encadre
	// le corps dans la signature en flux (aws-chunked), un cadrage que seuls
	// les services S3 réels décodent ; l'intégrité du contenu reste couverte par
	// SendContentMd5, qui fait voyager l'empreinte du corps avec la requête —
	// et la confidentialité, elle, relève de UseSSL, pas de la signature.
	if _, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(raw), int64(len(raw)),
		minio.PutObjectOptions{DisableMultipart: true, SendContentMd5: true, DisableContentSha256: true}); err != nil {
		return fmt.Errorf("storage : écriture du contenu de %s : %w", key, err)
	}

	return nil
}

// Open ouvre le contenu de la clé donnée, et rend l'erreur du domaine quand
// l'objet manque.
//
// GetObject est paresseux — l'erreur n'arriverait qu'à la première lecture —
// d'où le Stat immédiat : le contrat du port veut qu'une clé absente se voie à
// l'ouverture, pas au milieu d'une réponse HTTP déjà entamée.
func (s *S3) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}

	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage : ouverture du contenu de %s : %w", key, err)
	}

	if _, err := object.Stat(); err != nil {
		closeErr := object.Close()
		if isNoSuchKey(err) {
			return nil, fmt.Errorf("%w : clé %s", document.ErrContentNotFound, key)
		}
		return nil, fmt.Errorf("storage : lecture des attributs de %s : %w", key, errors.Join(err, closeErr))
	}

	return object, nil
}

// Delete supprime le contenu de la clé donnée. S3 rend déjà la suppression
// idempotente — effacer un objet absent réussit — ce qui est exactement le
// contrat du port ; le cas est tout de même traduit, pour un serveur qui
// répondrait NoSuchKey.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}

	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil && !isNoSuchKey(err) {
		return fmt.Errorf("storage : suppression du contenu de %s : %w", key, err)
	}

	return nil
}

// isNoSuchKey reconnaît la réponse « objet inexistant » du service, quelle que
// soit la façon dont la bibliothèque l'a construite — code S3 explicite, ou
// simple 404 d'un HEAD sans corps.
func isNoSuchKey(err error) bool {
	response := minio.ToErrorResponse(err)
	return response.Code == minio.NoSuchKey || response.StatusCode == http.StatusNotFound
}
