package config_test

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/platform/config"
)

// testOAuthSecret est une clé HMAC de test : elle a la longueur exigée et n'est
// utilisée nulle part ailleurs.
const testOAuthSecret = "cle-de-test-uniquement-pour-la-suite-de-tests"

// testS3Secret est la clé secrète S3 des tests, sans aucun usage réel.
const testS3Secret = "secret-s3-de-test-sans-usage-reel"

// validEnv est le plus petit environnement qui charge. Deux variables n'ont pas
// de valeur par défaut raisonnable : la base de données, et la clé HMAC du
// serveur d'autorisation — qu'aucune valeur engendrée au démarrage ne pourrait
// remplacer, puisqu'elle doit survivre au redémarrage.
func validEnv(overrides map[string]string) map[string]string {
	env := map[string]string{
		"AVANTI_DATABASE_URL": "postgres://avanti:change-me@localhost:5439/avanti?sslmode=disable",
		"AVANTI_OAUTH_SECRET": testOAuthSecret,
	}
	for k, v := range overrides {
		env[k] = v
	}
	return env
}

// lookupFrom rend une fonction de la signature de os.LookupEnv, adossée à une
// carte plutôt qu'à l'environnement du processus — ce qui laisse les tests
// tourner en parallèle.
func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(lookupFrom(validEnv(nil)))
	if err != nil {
		t.Fatalf("Load() a échoué sur un environnement minimal valide : %v", err)
	}

	if cfg.Environment != config.Development {
		t.Errorf("Environment = %q, attendu %q", cfg.Environment, config.Development)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, attendu \":8080\"", cfg.ListenAddr)
	}
	if got := cfg.BaseURL.String(); got != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, attendu \"http://localhost:8080\"", got)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, attendu info", cfg.LogLevel)
	}
	if cfg.LogFormat != config.LogText {
		t.Errorf("LogFormat = %q, attendu %q en développement", cfg.LogFormat, config.LogText)
	}
	if !cfg.MigrateOnStart {
		t.Error("MigrateOnStart = false, les migrations doivent tourner par défaut")
	}
	if cfg.DBConnectTimeout != 10*time.Second {
		t.Errorf("DBConnectTimeout = %v, attendu 10s", cfg.DBConnectTimeout)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, attendu 15s", cfg.ShutdownTimeout)
	}
	if !filepath.IsAbs(cfg.DocumentsDir) {
		t.Errorf("DocumentsDir = %q, doit être rendu absolu", cfg.DocumentsDir)
	}
	if cfg.StorageBackend != config.StorageFilesystem {
		t.Errorf("StorageBackend = %q, attendu %q par défaut", cfg.StorageBackend, config.StorageFilesystem)
	}
	if string(cfg.OAuthSecret) != testOAuthSecret {
		t.Errorf("OAuthSecret = %q, attendu la valeur fournie", cfg.OAuthSecret)
	}
}

// TestLogValueHidesOAuthSecret vérifie que la clé HMAC ne sort jamais dans les
// journaux, pas même tronquée : elle signe tous les jetons de l'instance, et un
// préfixe divulgué réduirait de plusieurs ordres de grandeur le coût d'une
// recherche.
func TestLogValueHidesOAuthSecret(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(lookupFrom(validEnv(nil)))
	if err != nil {
		t.Fatalf("Load() a échoué : %v", err)
	}

	rendered := cfg.LogValue().String()

	if strings.Contains(rendered, testOAuthSecret) {
		t.Errorf("LogValue() divulgue la clé HMAC : %s", rendered)
	}
	// Un préfixe de huit caractères suffirait déjà à réduire l'espace de
	// recherche : le test refuse aussi les divulgations partielles.
	if prefix := testOAuthSecret[:8]; strings.Contains(rendered, prefix) {
		t.Errorf("LogValue() divulgue un préfixe de la clé HMAC : %s", rendered)
	}
	if !strings.Contains(rendered, "oauth_secret_length") {
		t.Errorf("LogValue() devrait tout de même dire la longueur de la clé : %s", rendered)
	}
}

// TestLogValueHidesS3Credentials : la clé secrète S3 suit la règle de la clé
// HMAC — jamais dans les journaux — et l'identifiant d'accès non plus, par
// prudence : il est la moitié d'une paire d'identifiants.
func TestLogValueHidesS3Credentials(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(lookupFrom(validEnv(map[string]string{
		"AVANTI_STORAGE_BACKEND": "s3",
		"AVANTI_S3_ENDPOINT":     "s3.example.org:9000",
		"AVANTI_S3_BUCKET":       "avanti-documents",
		"AVANTI_S3_ACCESS_KEY":   "avanti-acces-test",
		"AVANTI_S3_SECRET_KEY":   testS3Secret,
	})))
	if err != nil {
		t.Fatalf("Load() a échoué : %v", err)
	}

	rendered := cfg.LogValue().String()

	for name, leaked := range map[string]string{
		"clé secrète":            testS3Secret,
		"préfixe de clé secrète": testS3Secret[:8],
		"identifiant d'accès":    "avanti-acces-test",
	} {
		if strings.Contains(rendered, leaked) {
			t.Errorf("LogValue() divulgue la %s S3 : %s", name, rendered)
		}
	}
	// Le reste doit rester exploitable pour diagnostiquer un branchement.
	if !strings.Contains(rendered, "s3.example.org:9000") || !strings.Contains(rendered, "avanti-documents") {
		t.Errorf("LogValue() = %s, l'adresse et le seau doivent y figurer", rendered)
	}
}

func TestLoadAccepts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, cfg *config.Config)
	}{
		{
			name: "production bascule les journaux en JSON",
			env:  map[string]string{"AVANTI_ENV": "production"},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.LogFormat != config.LogJSON {
					t.Errorf("LogFormat = %q, attendu %q", cfg.LogFormat, config.LogJSON)
				}
			},
		},
		{
			name: "le format de journal explicite l'emporte sur l'environnement",
			env:  map[string]string{"AVANTI_ENV": "production", "AVANTI_LOG_FORMAT": "TEXT"},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.LogFormat != config.LogText {
					t.Errorf("LogFormat = %q, attendu %q", cfg.LogFormat, config.LogText)
				}
			},
		},
		{
			name: "l'environnement est insensible à la casse",
			env:  map[string]string{"AVANTI_ENV": "Production"},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.Environment != config.Production {
					t.Errorf("Environment = %q, attendu %q", cfg.Environment, config.Production)
				}
			},
		},
		{
			name: "une adresse d'écoute complète est conservée",
			env:  map[string]string{"AVANTI_LISTEN_ADDR": "127.0.0.1:9000"},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.ListenAddr != "127.0.0.1:9000" {
					t.Errorf("ListenAddr = %q", cfg.ListenAddr)
				}
			},
		},
		{
			name: "la barre oblique finale de l'URL publique est retirée",
			env:  map[string]string{"AVANTI_BASE_URL": "https://avanti.example.org/chantier/"},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if got := cfg.BaseURL.String(); got != "https://avanti.example.org/chantier" {
					t.Errorf("BaseURL = %q", got)
				}
			},
		},
		{
			name: "une variable présente mais vide retombe sur la valeur par défaut",
			env:  map[string]string{"AVANTI_LISTEN_ADDR": "   "},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.ListenAddr != ":8080" {
					t.Errorf("ListenAddr = %q, attendu la valeur par défaut", cfg.ListenAddr)
				}
			},
		},
		{
			name: "les espaces de bordure sont ignorés",
			env:  map[string]string{"AVANTI_LOG_LEVEL": "  debug  "},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.LogLevel != slog.LevelDebug {
					t.Errorf("LogLevel = %v, attendu debug", cfg.LogLevel)
				}
			},
		},
		{
			name: "les migrations se désactivent",
			env:  map[string]string{"AVANTI_MIGRATE_ON_START": "false"},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.MigrateOnStart {
					t.Error("MigrateOnStart = true, attendu false")
				}
			},
		},
		{
			name: "un répertoire de documents absolu est conservé tel quel",
			env:  map[string]string{"AVANTI_DOCUMENTS_DIR": "/var/lib/avanti/documents"},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.DocumentsDir != "/var/lib/avanti/documents" {
					t.Errorf("DocumentsDir = %q", cfg.DocumentsDir)
				}
			},
		},
		{
			name: "le schéma postgresql:// est accepté au même titre que postgres://",
			env:  map[string]string{"AVANTI_DATABASE_URL": "postgresql://avanti@localhost:5439/avanti"},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.DatabaseURL == "" {
					t.Error("DatabaseURL vide")
				}
			},
		},
		{
			name: "le backend s3 se choisit, avec ses variables",
			env: map[string]string{
				"AVANTI_STORAGE_BACKEND": "S3",
				"AVANTI_S3_ENDPOINT":     "s3.example.org:9000",
				"AVANTI_S3_BUCKET":       "avanti-documents",
				"AVANTI_S3_ACCESS_KEY":   "avanti-acces-test",
				"AVANTI_S3_SECRET_KEY":   testS3Secret,
				"AVANTI_S3_REGION":       "garage",
			},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.StorageBackend != config.StorageS3 {
					t.Errorf("StorageBackend = %q", cfg.StorageBackend)
				}
				if cfg.S3Endpoint != "s3.example.org:9000" || cfg.S3Bucket != "avanti-documents" {
					t.Errorf("S3 = %q / %q", cfg.S3Endpoint, cfg.S3Bucket)
				}
				if cfg.S3AccessKey != "avanti-acces-test" || cfg.S3SecretKey != testS3Secret {
					t.Error("les identifiants S3 ne sont pas repris tels quels")
				}
				if cfg.S3Region != "garage" {
					t.Errorf("S3Region = %q", cfg.S3Region)
				}
				if !cfg.S3UseSSL {
					t.Error("S3UseSSL doit valoir true par défaut")
				}
			},
		},
		{
			name: "le TLS du S3 se désactive explicitement",
			env: map[string]string{
				"AVANTI_STORAGE_BACKEND": "s3",
				"AVANTI_S3_ENDPOINT":     "127.0.0.1:9000",
				"AVANTI_S3_BUCKET":       "avanti-documents",
				"AVANTI_S3_ACCESS_KEY":   "avanti-acces-test",
				"AVANTI_S3_SECRET_KEY":   testS3Secret,
				"AVANTI_S3_USE_SSL":      "false",
			},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.S3UseSSL {
					t.Error("S3UseSSL = true, attendu false")
				}
			},
		},
		{
			name: "les variables S3 sont ignorées avec le backend filesystem",
			env: map[string]string{
				"AVANTI_STORAGE_BACKEND": "filesystem",
				"AVANTI_S3_ENDPOINT":     "s3.example.org:9000",
			},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.S3Endpoint != "" {
					t.Errorf("S3Endpoint = %q, attendu vide hors backend s3", cfg.S3Endpoint)
				}
			},
		},
		{
			name: "les délais d'attente se règlent",
			env: map[string]string{
				"AVANTI_DB_CONNECT_TIMEOUT": "3s",
				"AVANTI_SHUTDOWN_TIMEOUT":   "1m30s",
			},
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.DBConnectTimeout != 3*time.Second {
					t.Errorf("DBConnectTimeout = %v", cfg.DBConnectTimeout)
				}
				if cfg.ShutdownTimeout != 90*time.Second {
					t.Errorf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Load(lookupFrom(validEnv(tc.env)))
			if err != nil {
				t.Fatalf("Load() a échoué : %v", err)
			}
			tc.check(t, cfg)
		})
	}
}

func TestLoadRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  map[string]string
		// want est un fragment que le message d'erreur doit contenir : c'est la
		// variable fautive qui compte, l'exploitant doit la trouver sans lire le
		// code.
		want string
	}{
		{
			name: "base de données absente",
			env:  map[string]string{"AVANTI_DATABASE_URL": ""},
			want: "AVANTI_DATABASE_URL",
		},
		{
			name: "schéma de base de données inattendu",
			env:  map[string]string{"AVANTI_DATABASE_URL": "mysql://avanti@localhost/avanti"},
			want: "AVANTI_DATABASE_URL",
		},
		{
			name: "clé HMAC OAuth absente",
			env:  map[string]string{"AVANTI_OAUTH_SECRET": ""},
			want: "AVANTI_OAUTH_SECRET",
		},
		{
			name: "clé HMAC OAuth trop courte",
			env:  map[string]string{"AVANTI_OAUTH_SECRET": "trop-courte"},
			want: "AVANTI_OAUTH_SECRET",
		},
		{
			// La valeur d'exemple de .env.example passe en développement et échoue
			// en production : c'est le seul contrôle de configuration qui dépende de
			// l'environnement, et il vaut la peine d'être verrouillé par un test.
			name: "valeur d'exemple de la clé HMAC OAuth en production",
			env: map[string]string{
				"AVANTI_ENV":          "production",
				"AVANTI_OAUTH_SECRET": "change-me-remplacez-par-openssl-rand-base64-32",
			},
			want: "AVANTI_OAUTH_SECRET",
		},
		{
			name: "environnement inconnu",
			env:  map[string]string{"AVANTI_ENV": "staging"},
			want: "AVANTI_ENV",
		},
		{
			name: "backend de stockage inconnu",
			env:  map[string]string{"AVANTI_STORAGE_BACKEND": "disquette"},
			want: "AVANTI_STORAGE_BACKEND",
		},
		{
			name: "backend s3 sans adresse",
			env: map[string]string{
				"AVANTI_STORAGE_BACKEND": "s3",
				"AVANTI_S3_BUCKET":       "avanti-documents",
				"AVANTI_S3_ACCESS_KEY":   "avanti-acces-test",
				"AVANTI_S3_SECRET_KEY":   testS3Secret,
			},
			want: "AVANTI_S3_ENDPOINT",
		},
		{
			name: "backend s3 sans seau",
			env: map[string]string{
				"AVANTI_STORAGE_BACKEND": "s3",
				"AVANTI_S3_ENDPOINT":     "s3.example.org:9000",
				"AVANTI_S3_ACCESS_KEY":   "avanti-acces-test",
				"AVANTI_S3_SECRET_KEY":   testS3Secret,
			},
			want: "AVANTI_S3_BUCKET",
		},
		{
			name: "backend s3 sans clé d'accès",
			env: map[string]string{
				"AVANTI_STORAGE_BACKEND": "s3",
				"AVANTI_S3_ENDPOINT":     "s3.example.org:9000",
				"AVANTI_S3_BUCKET":       "avanti-documents",
				"AVANTI_S3_SECRET_KEY":   testS3Secret,
			},
			want: "AVANTI_S3_ACCESS_KEY",
		},
		{
			name: "backend s3 sans clé secrète",
			env: map[string]string{
				"AVANTI_STORAGE_BACKEND": "s3",
				"AVANTI_S3_ENDPOINT":     "s3.example.org:9000",
				"AVANTI_S3_BUCKET":       "avanti-documents",
				"AVANTI_S3_ACCESS_KEY":   "avanti-acces-test",
			},
			want: "AVANTI_S3_SECRET_KEY",
		},
		{
			name: "booléen S3_USE_SSL mal orthographié",
			env: map[string]string{
				"AVANTI_STORAGE_BACKEND": "s3",
				"AVANTI_S3_ENDPOINT":     "s3.example.org:9000",
				"AVANTI_S3_BUCKET":       "avanti-documents",
				"AVANTI_S3_ACCESS_KEY":   "avanti-acces-test",
				"AVANTI_S3_SECRET_KEY":   testS3Secret,
				"AVANTI_S3_USE_SSL":      "oui",
			},
			want: "AVANTI_S3_USE_SSL",
		},
		{
			name: "adresse d'écoute sans port",
			env:  map[string]string{"AVANTI_LISTEN_ADDR": "localhost"},
			want: "AVANTI_LISTEN_ADDR",
		},
		{
			name: "adresse d'écoute au port vide",
			env:  map[string]string{"AVANTI_LISTEN_ADDR": "localhost:"},
			want: "AVANTI_LISTEN_ADDR",
		},
		{
			name: "URL publique relative",
			env:  map[string]string{"AVANTI_BASE_URL": "/chantier"},
			want: "AVANTI_BASE_URL",
		},
		{
			name: "URL publique sans hôte",
			env:  map[string]string{"AVANTI_BASE_URL": "http://"},
			want: "AVANTI_BASE_URL",
		},
		{
			name: "niveau de journal inconnu",
			env:  map[string]string{"AVANTI_LOG_LEVEL": "verbeux"},
			want: "AVANTI_LOG_LEVEL",
		},
		{
			name: "format de journal inconnu",
			env:  map[string]string{"AVANTI_LOG_FORMAT": "xml"},
			want: "AVANTI_LOG_FORMAT",
		},
		{
			name: "booléen mal orthographié",
			env:  map[string]string{"AVANTI_MIGRATE_ON_START": "oui"},
			want: "AVANTI_MIGRATE_ON_START",
		},
		{
			name: "durée sans unité",
			env:  map[string]string{"AVANTI_SHUTDOWN_TIMEOUT": "15"},
			want: "AVANTI_SHUTDOWN_TIMEOUT",
		},
		{
			name: "durée nulle",
			env:  map[string]string{"AVANTI_DB_CONNECT_TIMEOUT": "0s"},
			want: "AVANTI_DB_CONNECT_TIMEOUT",
		},
		{
			name: "durée négative",
			env:  map[string]string{"AVANTI_DB_CONNECT_TIMEOUT": "-1s"},
			want: "AVANTI_DB_CONNECT_TIMEOUT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Load(lookupFrom(validEnv(tc.env)))
			if err == nil {
				t.Fatalf("Load() a accepté un environnement invalide, config = %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("Load() renvoie une configuration non nulle avec une erreur : %+v", cfg)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message = %q, doit mentionner %q", err.Error(), tc.want)
			}
		})
	}
}

// TestLoadReportsEveryProblemAtOnce protège la promesse la plus utile du
// package : un exploitant qui a fait trois fautes les voit toutes du premier
// coup, sans redémarrer trois fois.
func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()

	_, err := config.Load(lookupFrom(map[string]string{
		"AVANTI_ENV":         "staging",
		"AVANTI_LISTEN_ADDR": "localhost",
		"AVANTI_LOG_LEVEL":   "verbeux",
	}))
	if err == nil {
		t.Fatal("Load() doit échouer")
	}

	for _, want := range []string{
		"AVANTI_DATABASE_URL",
		"AVANTI_OAUTH_SECRET",
		"AVANTI_ENV",
		"AVANTI_LISTEN_ADDR",
		"AVANTI_LOG_LEVEL",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message = %q, doit mentionner %q", err.Error(), want)
		}
	}
}

func TestLoadRefusesNilLookup(t *testing.T) {
	t.Parallel()

	if _, err := config.Load(nil); err == nil {
		t.Fatal("Load(nil) doit échouer plutôt que de paniquer")
	}
}

// TestLogValueRedactsPassword vérifie qu'aucun mot de passe ne peut atteindre
// les journaux par le chemin normal, celui du démarrage.
func TestLogValueRedactsPassword(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(lookupFrom(validEnv(map[string]string{
		"AVANTI_DATABASE_URL": "postgres://avanti:tr3s-secret@db.example.org:5432/avanti",
	})))
	if err != nil {
		t.Fatalf("Load() a échoué : %v", err)
	}

	rendered := cfg.LogValue().String()
	if strings.Contains(rendered, "tr3s-secret") {
		t.Errorf("LogValue() laisse fuir le mot de passe : %s", rendered)
	}
	if !strings.Contains(rendered, "db.example.org") {
		t.Errorf("LogValue() = %s, doit rester exploitable pour diagnostiquer", rendered)
	}
}
