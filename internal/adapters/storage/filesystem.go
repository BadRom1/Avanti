package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Romain-Badino/Avanti/internal/document"
)

// Permissions du stockage : le contenu est confidentiel (devis, finances,
// documents d'assurance), seul le compte du service a quelque chose à y faire.
const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// Filesystem implémente [document.Storage] sur un répertoire local. C'est
// l'implémentation par défaut, cohérente avec un déploiement self-hosted :
// rien à installer, et une sauvegarde du répertoire suffit.
//
// Un fichier par pièce, nommé par sa clé — un UUID validé par [checkKey]
// avant tout usage. La traversée de chemin est empêchée deux fois : par cette
// validation de forme, et par [os.Root], qui borne toutes les opérations au
// répertoire au niveau du système (voir key.go).
type Filesystem struct {
	dir string
	// root borne les opérations fichier au répertoire de stockage. Il vit
	// aussi longtemps que le processus, comme un pool de connexions : le port
	// document.Storage n'a pas de cycle de vie à lui offrir, et un descripteur
	// tenu ouvert est exactement ce qu'on attend d'un stockage branché.
	root *os.Root
}

// NewFilesystem construit le stockage sur le répertoire donné, créé en 0700
// s'il manque.
func NewFilesystem(dir string) (*Filesystem, error) {
	if dir == "" {
		return nil, errors.New("storage : répertoire de stockage manquant")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("storage : création du répertoire %s : %w", dir, err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("storage : ouverture du répertoire %s : %w", dir, err)
	}

	return &Filesystem{dir: dir, root: root}, nil
}

// Save écrit le contenu sous la clé donnée, et refuse une clé déjà occupée.
//
// L'écriture est en deux temps pour être atomique : un fichier temporaire dans
// le même répertoire — donc sur le même système de fichiers — reçoit tout le
// contenu, puis un lien dur le publie sous son nom définitif. Un lien plutôt
// qu'un rename, et la nuance est le contrat : rename écrase une cible
// existante, link échoue si elle existe — c'est à la fois la publication
// atomique et le refus de la clé occupée, en une seule opération que deux
// écritures simultanées ne peuvent pas gagner toutes les deux. Une panne en
// cours de route ne laisse au pire qu'un fichier temporaire, jamais un contenu
// tronqué sous une clé publiée.
func (f *Filesystem) Save(_ context.Context, key string, content io.Reader) error {
	if err := checkKey(key); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(f.dir, key+".tmp-*")
	if err != nil {
		return fmt.Errorf("storage : création du fichier temporaire pour %s : %w", key, err)
	}
	defer func() {
		// Le ménage du chemin d'erreur : après une publication réussie, le
		// temporaire a déjà été retiré et ce Remove ne trouve rien.
		_ = tmp.Close()           //nolint:errcheck // déjà fermé sur le chemin heureux, filet du chemin d'erreur.
		_ = os.Remove(tmp.Name()) //nolint:errcheck // meilleur effort : un temporaire orphelin est un déchet, pas une incohérence.
	}()

	if err := tmp.Chmod(filePerm); err != nil {
		return fmt.Errorf("storage : permissions du fichier temporaire de %s : %w", key, err)
	}
	if _, err := io.Copy(tmp, content); err != nil {
		return fmt.Errorf("storage : écriture du contenu de %s : %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage : fermeture du contenu de %s : %w", key, err)
	}

	if err := f.root.Link(filepath.Base(tmp.Name()), key); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w : clé %s", document.ErrContentAlreadyExists, key)
		}
		return fmt.Errorf("storage : publication du contenu de %s : %w", key, err)
	}

	if err := os.Remove(tmp.Name()); err != nil {
		return fmt.Errorf("storage : suppression du fichier temporaire de %s : %w", key, err)
	}

	return nil
}

// Open ouvre le contenu de la clé donnée, et rend l'erreur du domaine quand le
// fichier manque.
func (f *Filesystem) Open(_ context.Context, key string) (io.ReadCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}

	file, err := f.root.Open(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w : clé %s", document.ErrContentNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("storage : ouverture du contenu de %s : %w", key, err)
	}

	return file, nil
}

// Delete supprime le contenu de la clé donnée. Une clé absente n'est pas une
// erreur : c'est ce qui rend le nettoyage de secours du domaine rejouable.
func (f *Filesystem) Delete(_ context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}

	if err := f.root.Remove(key); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage : suppression du contenu de %s : %w", key, err)
	}

	return nil
}
