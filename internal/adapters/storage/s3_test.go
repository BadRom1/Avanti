package storage_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Romain-Badino/Avanti/internal/adapters/storage"
	"github.com/Romain-Badino/Avanti/internal/document"
)

// bucketTest est le seau que le faux service expose.
const bucketTest = "avanti-documents"

// fakeS3 est un service compatible S3 minimal, en mémoire, monté sur
// httptest.Server : PUT dépose, GET lit, HEAD interroge, DELETE supprime.
//
// Il ignore délibérément l'authentification — c'est le comportement de
// l'adapter qu'on teste, pas la signature sigv4, qui appartient à la
// bibliothèque cliente. Il ne parle que le style d'URL par chemin
// (/seau/clé), celui que le client emploie face à une adresse IP.
type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	requests []string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: make(map[string][]byte)}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, r.Method+" "+r.URL.Path)

	// La découverte de région (« GET /seau?location= ») reçoit une réponse
	// vide et valide : le faux service n'a qu'une région, comme la plupart des
	// S3 auto-hébergés.
	if r.URL.Query().Has("location") {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0"?><LocationConstraint></LocationConstraint>`) //nolint:errcheck // faux serveur de test.
		return
	}

	key, ok := strings.CutPrefix(r.URL.Path, "/"+bucketTest+"/")
	if !ok || key == "" {
		http.Error(w, "seau inconnu", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.objects[key] = raw
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		raw, exists := f.objects[key]
		if !exists {
			// Un HEAD n'a pas de corps : le client déduit NoSuchKey du seul 404.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		raw, exists := f.objects[key]
		if !exists {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>clé absente</Message></Error>`) //nolint:errcheck // faux serveur de test.
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		_, _ = w.Write(raw) //nolint:errcheck // faux serveur de test.
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "méthode inattendue", http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// newS3 monte l'adapter sur un faux service neuf.
func newS3(t *testing.T) (*storage.S3, *fakeS3) {
	t.Helper()

	fake := newFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("URL du faux service illisible : %v", err)
	}

	adapter, err := storage.NewS3(storage.S3Options{
		Endpoint:  endpoint.Host,
		Bucket:    bucketTest,
		AccessKey: "avanti-test",
		SecretKey: "sans-aucun-usage-reel",
		UseSSL:    false,
	})
	if err != nil {
		t.Fatalf("NewS3() échoué : %v", err)
	}

	return adapter, fake
}

func TestNewS3RejectsIncompleteOptions(t *testing.T) {
	t.Parallel()

	valid := storage.S3Options{
		Endpoint: "s3.example.org", Bucket: bucketTest,
		AccessKey: "ak", SecretKey: "sk",
	}

	cases := map[string]func(*storage.S3Options){
		"adresse manquante":     func(o *storage.S3Options) { o.Endpoint = "" },
		"seau manquant":         func(o *storage.S3Options) { o.Bucket = "" },
		"clé d'accès manquante": func(o *storage.S3Options) { o.AccessKey = "" },
		"clé secrète manquante": func(o *storage.S3Options) { o.SecretKey = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := valid
			mutate(&opts)
			if _, err := storage.NewS3(opts); err == nil {
				t.Error("NewS3() doit refuser une option manquante")
			}
		})
	}
}

// TestS3RoundTrip : ce qui est écrit se relit à l'identique, en passant par le
// protocole réel — requêtes HTTP comprises.
func TestS3RoundTrip(t *testing.T) {
	t.Parallel()

	adapter, fake := newS3(t)

	if err := adapter.Save(t.Context(), testUUID, strings.NewReader("contenu du devis")); err != nil {
		t.Fatalf("Save() échoué : %v", err)
	}
	if got := string(fake.objects[testUUID]); got != "contenu du devis" {
		t.Fatalf("objet stocké = %q", got)
	}

	content, err := adapter.Open(t.Context(), testUUID)
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
}

// TestS3SaveRefusesTakenKey : le StatObject préalable tient le contrat du
// port, et le contenu d'origine reste intact.
func TestS3SaveRefusesTakenKey(t *testing.T) {
	t.Parallel()

	adapter, fake := newS3(t)

	if err := adapter.Save(t.Context(), testUUID, strings.NewReader("original")); err != nil {
		t.Fatalf("premier Save() échoué : %v", err)
	}
	if err := adapter.Save(t.Context(), testUUID, strings.NewReader("écrasement")); !errors.Is(err, document.ErrContentAlreadyExists) {
		t.Fatalf("second Save() = %v, attendu ErrContentAlreadyExists", err)
	}
	if got := string(fake.objects[testUUID]); got != "original" {
		t.Errorf("le contenu d'origine a été écrasé : %q", got)
	}
}

// TestS3OpenMissingKey : l'objet absent est traduit dans l'erreur du domaine,
// à l'ouverture et non à la première lecture.
func TestS3OpenMissingKey(t *testing.T) {
	t.Parallel()

	adapter, _ := newS3(t)

	if _, err := adapter.Open(t.Context(), testUUID); !errors.Is(err, document.ErrContentNotFound) {
		t.Fatalf("Open(absent) = %v, attendu ErrContentNotFound", err)
	}
}

// TestS3DeleteIsIdempotent : même contrat que le stockage disque.
func TestS3DeleteIsIdempotent(t *testing.T) {
	t.Parallel()

	adapter, fake := newS3(t)

	if err := adapter.Save(t.Context(), testUUID, strings.NewReader("contenu")); err != nil {
		t.Fatalf("Save() échoué : %v", err)
	}
	if err := adapter.Delete(t.Context(), testUUID); err != nil {
		t.Fatalf("Delete() échoué : %v", err)
	}
	if _, exists := fake.objects[testUUID]; exists {
		t.Error("l'objet n'a pas été supprimé")
	}
	if err := adapter.Delete(t.Context(), testUUID); err != nil {
		t.Errorf("second Delete() = %v, la suppression doit être idempotente", err)
	}
}

// TestS3RejectsMaliciousKeys : la validation de forme refuse la clé avant
// toute requête — aucun octet ne part vers le service.
func TestS3RejectsMaliciousKeys(t *testing.T) {
	t.Parallel()

	adapter, fake := newS3(t)

	for _, key := range []string{"", "../autre-seau/objet", "objet?x=1", testUUID + "/suffixe", strings.ToUpper(testUUID)} {
		if err := adapter.Save(t.Context(), key, strings.NewReader("x")); err == nil {
			t.Errorf("Save(%q) accepté", key)
		}
		if _, err := adapter.Open(t.Context(), key); err == nil {
			t.Errorf("Open(%q) accepté", key)
		}
		if err := adapter.Delete(t.Context(), key); err == nil {
			t.Errorf("Delete(%q) accepté", key)
		}
	}

	if count := fake.requestCount(); count != 0 {
		t.Errorf("%d requête(s) émise(s) pour des clés refusées en forme", count)
	}
}

// TestS3SaveRefusesOversizedStream : la taille réelle du flux est vérifiée
// contre la borne du domaine, indépendamment de la taille annoncée en amont.
func TestS3SaveRefusesOversizedStream(t *testing.T) {
	t.Parallel()

	adapter, fake := newS3(t)

	oversized := io.LimitReader(zeroReader{}, document.MaxFileSize+1)
	if err := adapter.Save(t.Context(), testUUID, oversized); !errors.Is(err, document.ErrFileTooLarge) {
		t.Fatalf("Save(flux trop long) = %v, attendu ErrFileTooLarge", err)
	}
	if _, exists := fake.objects[testUUID]; exists {
		t.Error("un contenu hors borne a été stocké")
	}
}

// zeroReader rend des zéros sans fin, pour fabriquer un flux hors borne sans
// allouer le tampon correspondant côté test.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
