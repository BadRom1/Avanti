package storage_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/adapters/storage"
	"github.com/Romain-Badino/Avanti/internal/document"
)

// testUUID est une clé de la forme exigée : un UUID canonique. Le nom évite
// « key » : gitleaks prendrait la constante pour un secret, ce qu'un UUID de
// test n'est pas.
const testUUID = "6bbd562d-51ab-4bee-ab5b-1b9ec2a08ec5"

func newFilesystem(t *testing.T) (fsys *storage.Filesystem, dir string) {
	t.Helper()

	dir = filepath.Join(t.TempDir(), "documents")
	fsys, err := storage.NewFilesystem(dir)
	if err != nil {
		t.Fatalf("NewFilesystem() échoué : %v", err)
	}

	return fsys, dir
}

func TestNewFilesystemRejectsEmptyDir(t *testing.T) {
	t.Parallel()

	if _, err := storage.NewFilesystem(""); err == nil {
		t.Error("NewFilesystem(\"\") doit échouer")
	}
}

// TestNewFilesystemCreatesPrivateDir : le répertoire manquant est créé, et en
// 0700 — le contenu est confidentiel.
func TestNewFilesystemCreatesPrivateDir(t *testing.T) {
	t.Parallel()

	_, dir := newFilesystem(t)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("répertoire non créé : %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions du répertoire = %o, attendu 700", perm)
	}
}

// TestFilesystemRoundTrip : ce qui est écrit se relit à l'identique, et le
// fichier est en 0600.
func TestFilesystemRoundTrip(t *testing.T) {
	t.Parallel()

	fsys, dir := newFilesystem(t)

	if err := fsys.Save(t.Context(), testUUID, strings.NewReader("contenu du devis")); err != nil {
		t.Fatalf("Save() échoué : %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, testUUID))
	if err != nil {
		t.Fatalf("fichier absent après Save : %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions du fichier = %o, attendu 600", perm)
	}

	content, err := fsys.Open(t.Context(), testUUID)
	if err != nil {
		t.Fatalf("Open() échoué : %v", err)
	}
	defer func() {
		if closeErr := content.Close(); closeErr != nil {
			t.Errorf("fermeture du contenu : %v", closeErr)
		}
	}()

	raw, err := io.ReadAll(content)
	if err != nil {
		t.Fatalf("lecture du contenu : %v", err)
	}
	if string(raw) != "contenu du devis" {
		t.Errorf("contenu = %q", raw)
	}

	// Aucun fichier temporaire ne survit au chemin heureux.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("lecture du répertoire : %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("le répertoire contient %d entrées, attendu le seul fichier publié", len(entries))
	}
}

// TestFilesystemSaveRefusesTakenKey : une clé occupée est refusée avec
// l'erreur du domaine, et le contenu d'origine reste intact.
func TestFilesystemSaveRefusesTakenKey(t *testing.T) {
	t.Parallel()

	fsys, dir := newFilesystem(t)

	if err := fsys.Save(t.Context(), testUUID, strings.NewReader("original")); err != nil {
		t.Fatalf("premier Save() échoué : %v", err)
	}
	if err := fsys.Save(t.Context(), testUUID, strings.NewReader("écrasement")); !errors.Is(err, document.ErrContentAlreadyExists) {
		t.Fatalf("second Save() = %v, attendu ErrContentAlreadyExists", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, testUUID))
	if err != nil {
		t.Fatalf("relecture : %v", err)
	}
	if string(raw) != "original" {
		t.Errorf("le contenu d'origine a été écrasé : %q", raw)
	}
}

// TestFilesystemRejectsMaliciousKeys : tout ce qui n'est pas exactement un
// UUID est refusé avant de toucher au moindre chemin — c'est la défense contre
// la traversée.
func TestFilesystemRejectsMaliciousKeys(t *testing.T) {
	t.Parallel()

	fsys, dir := newFilesystem(t)

	// Un appât hors du répertoire : si une traversée passait, c'est lui qu'elle
	// atteindrait.
	bait := filepath.Join(filepath.Dir(dir), "appat.txt")
	if err := os.WriteFile(bait, []byte("hors du stockage"), 0o600); err != nil {
		t.Fatalf("écriture de l'appât : %v", err)
	}

	keys := []string{
		"",
		"../appat.txt",
		"..",
		"/etc/passwd",
		`..\appat.txt`,
		"sous/" + testUUID,
		testUUID + "/../appat.txt",
		// Un UUID déguisé : préfixe valide, suffixe qui traverse.
		testUUID + "-x",
		strings.ToUpper(testUUID),
		"6BBD562D-51ab-4bee-ab5b-1b9ec2a08ec5",
	}

	for _, key := range keys {
		if err := fsys.Save(t.Context(), key, strings.NewReader("x")); err == nil || errors.Is(err, document.ErrContentAlreadyExists) {
			t.Errorf("Save(%q) = %v, une clé malveillante doit être refusée en forme", key, err)
		}
		if _, err := fsys.Open(t.Context(), key); err == nil || errors.Is(err, document.ErrContentNotFound) {
			t.Errorf("Open(%q) = %v, une clé malveillante doit être refusée en forme", key, err)
		}
		if err := fsys.Delete(t.Context(), key); err == nil {
			t.Errorf("Delete(%q) accepté, une clé malveillante doit être refusée en forme", key)
		}
	}

	if _, err := os.Stat(bait); err != nil {
		t.Errorf("l'appât hors du répertoire a été touché : %v", err)
	}
}

// TestFilesystemOpenMissingKey : un fichier manquant rend l'erreur du domaine,
// que le service traduit en incohérence.
func TestFilesystemOpenMissingKey(t *testing.T) {
	t.Parallel()

	fsys, _ := newFilesystem(t)

	if _, err := fsys.Open(t.Context(), testUUID); !errors.Is(err, document.ErrContentNotFound) {
		t.Fatalf("Open(absent) = %v, attendu ErrContentNotFound", err)
	}
}

// TestFilesystemDeleteIsIdempotent : supprimer, puis supprimer encore — la
// seconde passe ne trouve rien et n'échoue pas, comme le contrat du port le
// demande.
func TestFilesystemDeleteIsIdempotent(t *testing.T) {
	t.Parallel()

	fsys, _ := newFilesystem(t)

	if err := fsys.Save(t.Context(), testUUID, strings.NewReader("contenu")); err != nil {
		t.Fatalf("Save() échoué : %v", err)
	}
	if err := fsys.Delete(t.Context(), testUUID); err != nil {
		t.Fatalf("Delete() échoué : %v", err)
	}
	if _, err := fsys.Open(t.Context(), testUUID); !errors.Is(err, document.ErrContentNotFound) {
		t.Fatalf("Open() après Delete = %v, attendu ErrContentNotFound", err)
	}
	if err := fsys.Delete(t.Context(), testUUID); err != nil {
		t.Errorf("second Delete() = %v, la suppression doit être idempotente", err)
	}
}
