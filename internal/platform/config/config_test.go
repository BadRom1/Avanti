package config_test

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Romain-Badino/Avanti/internal/platform/config"
)

// validEnv est le plus petit environnement qui charge : seule la base de données
// n'a pas de valeur par défaut raisonnable.
func validEnv(overrides map[string]string) map[string]string {
	env := map[string]string{
		"AVANTI_DATABASE_URL": "postgres://avanti:change-me@localhost:5439/avanti?sslmode=disable",
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
			name: "environnement inconnu",
			env:  map[string]string{"AVANTI_ENV": "staging"},
			want: "AVANTI_ENV",
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
