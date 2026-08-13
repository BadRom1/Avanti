// Package config lit et valide la configuration d'Avanti.
//
// Toutes les variables d'environnement portent le préfixe AVANTI_. La lecture a
// lieu une seule fois, au démarrage : le reste du dépôt reçoit un *Config en
// paramètre et n'appelle jamais os.Getenv lui-même (R1 et R3 de
// docs/ARCHITECTURE.md).
//
// La validation est groupée à dessein. Un démarrage qui échoue énumère *toutes*
// les variables fautives d'un coup, plutôt que d'obliger l'exploitant à
// redémarrer autant de fois qu'il a fait de fautes de frappe.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Prefix est le préfixe commun à toutes les variables d'environnement lues ici.
const Prefix = "AVANTI_"

// Environment distingue un poste de développement d'un déploiement réel. Il ne
// sert qu'à choisir des valeurs par défaut plus commodes ici ou plus prudentes
// là ; aucun comportement métier n'en dépend.
type Environment string

// Les environnements reconnus.
const (
	Development Environment = "development"
	Production  Environment = "production"
)

// LogFormat désigne le rendu des journaux : lisible à l'œil ou indexable par une
// machine.
type LogFormat string

// Les formats de journal reconnus.
const (
	LogText LogFormat = "text"
	LogJSON LogFormat = "json"
)

// Config rassemble tout ce qu'Avanti a besoin de savoir de son environnement
// d'exécution. Une instance valide ne sort que de Load.
type Config struct {
	// Environment vaut Development ou Production.
	Environment Environment
	// ListenAddr est l'adresse d'écoute HTTP, au format hôte:port.
	ListenAddr string
	// BaseURL est l'URL publique sous laquelle l'instance est jointe. Elle sert
	// à fabriquer les liens absolus (courriels, redirections OAuth) : elle peut
	// donc différer de ListenAddr quand un reverse proxy est devant.
	BaseURL *url.URL
	// DatabaseURL est la chaîne de connexion PostgreSQL.
	DatabaseURL string
	// DocumentsDir est le répertoire, toujours absolu, où l'adapter de stockage
	// dépose le contenu binaire des pièces du dossier.
	DocumentsDir string
	// LogLevel est le seuil de journalisation.
	LogLevel slog.Level
	// LogFormat est le rendu des journaux.
	LogFormat LogFormat
	// MigrateOnStart commande l'exécution des migrations au démarrage.
	MigrateOnStart bool
	// DBConnectTimeout borne la première prise de contact avec PostgreSQL.
	DBConnectTimeout time.Duration
	// ShutdownTimeout borne l'arrêt gracieux du serveur HTTP.
	ShutdownTimeout time.Duration
}

// Noms des variables d'environnement, sans leur préfixe.
const (
	keyEnv              = "ENV"
	keyListenAddr       = "LISTEN_ADDR"
	keyBaseURL          = "BASE_URL"
	keyDatabaseURL      = "DATABASE_URL"
	keyDocumentsDir     = "DOCUMENTS_DIR"
	keyLogLevel         = "LOG_LEVEL"
	keyLogFormat        = "LOG_FORMAT"
	keyMigrateOnStart   = "MIGRATE_ON_START"
	keyDBConnectTimeout = "DB_CONNECT_TIMEOUT"
	keyShutdownTimeout  = "SHUTDOWN_TIMEOUT"
)

// Valeurs par défaut, reprises telles quelles dans .env.example.
const (
	defaultListenAddr       = ":8080"
	defaultBaseURL          = "http://localhost:8080"
	defaultDocumentsDir     = "./data/documents"
	defaultLogLevel         = "info"
	defaultMigrateOnStart   = true
	defaultDBConnectTimeout = 10 * time.Second
	defaultShutdownTimeout  = 15 * time.Second
)

// LoadFromEnv lit la configuration dans l'environnement du processus.
func LoadFromEnv() (*Config, error) {
	return Load(os.LookupEnv)
}

// Load lit la configuration au travers de lookup, qui a la signature de
// os.LookupEnv. L'injecter rend la configuration testable sans toucher à
// l'environnement du processus, donc sans interdire le parallélisme des tests.
//
// L'erreur renvoyée agrège tous les problèmes rencontrés ; la déplier avec
// errors.Join se fait naturellement à l'affichage, chaque cause tenant sur une
// ligne.
func Load(lookup func(string) (string, bool)) (*Config, error) {
	if lookup == nil {
		return nil, errors.New("config : aucune source de variables d'environnement fournie")
	}

	l := &loader{lookup: lookup}

	env := l.environment()
	cfg := &Config{
		Environment:      env,
		ListenAddr:       l.listenAddr(),
		BaseURL:          l.baseURL(),
		DatabaseURL:      l.databaseURL(),
		DocumentsDir:     l.documentsDir(),
		LogLevel:         l.logLevel(),
		LogFormat:        l.logFormat(env),
		MigrateOnStart:   l.boolean(keyMigrateOnStart, defaultMigrateOnStart),
		DBConnectTimeout: l.duration(keyDBConnectTimeout, defaultDBConnectTimeout),
		ShutdownTimeout:  l.duration(keyShutdownTimeout, defaultShutdownTimeout),
	}

	if err := errors.Join(l.errs...); err != nil {
		return nil, fmt.Errorf("configuration invalide :\n%w", err)
	}

	return cfg, nil
}

// LogValue rend la configuration journalisable sans divulguer de secret : la
// chaîne de connexion PostgreSQL contient un mot de passe, elle sort caviardée.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("environment", string(c.Environment)),
		slog.String("listen_addr", c.ListenAddr),
		slog.String("base_url", c.BaseURL.String()),
		slog.String("database_url", redactDSN(c.DatabaseURL)),
		slog.String("documents_dir", c.DocumentsDir),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", string(c.LogFormat)),
		slog.Bool("migrate_on_start", c.MigrateOnStart),
		slog.Duration("db_connect_timeout", c.DBConnectTimeout),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
	)
}

// redactDSN remplace le mot de passe d'une chaîne de connexion par des
// astérisques. Une chaîne illisible est remplacée en bloc : mieux vaut perdre
// l'information que risquer d'en recracher une partie sensible dans un journal.
func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "(chaîne de connexion illisible)"
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}

// loader porte la source des variables et accumule les erreurs de validation.
type loader struct {
	lookup func(string) (string, bool)
	errs   []error
}

// reject enregistre un problème sur la variable key sans interrompre la lecture,
// pour que Load puisse les rapporter toutes ensemble.
func (l *loader) reject(key, problem string) {
	l.errs = append(l.errs, fmt.Errorf("%s%s : %s", Prefix, key, problem))
}

// raw renvoie la valeur brute de la variable, débarrassée de ses espaces de
// bordure, et indique si elle était renseignée. Une variable présente mais vide
// est traitée comme absente : c'est ce que produit un .env où la ligne a été
// laissée en plan.
func (l *loader) raw(key string) (string, bool) {
	value, ok := l.lookup(Prefix + key)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// str renvoie la valeur de la variable ou def si elle est absente.
func (l *loader) str(key, def string) string {
	if value, ok := l.raw(key); ok {
		return value
	}
	return def
}

// required renvoie la valeur de la variable et signale son absence.
func (l *loader) required(key string) string {
	value, ok := l.raw(key)
	if !ok {
		l.reject(key, "variable obligatoire, elle doit être renseignée")
	}
	return value
}

// boolean lit un booléen au sens de strconv.ParseBool (true/false, 1/0, yes et
// no exceptés).
func (l *loader) boolean(key string, def bool) bool {
	value, ok := l.raw(key)
	if !ok {
		return def
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		l.reject(key, fmt.Sprintf("booléen attendu (true ou false), reçu %q", value))
		return def
	}
	return parsed
}

// duration lit une durée au format Go (« 10s », « 1m30s ») et refuse les valeurs
// nulles ou négatives : un délai d'attente de zéro n'a jamais le sens qu'on croit.
func (l *loader) duration(key string, def time.Duration) time.Duration {
	value, ok := l.raw(key)
	if !ok {
		return def
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		l.reject(key, fmt.Sprintf("durée attendue au format Go, par exemple 10s ou 1m30s, reçu %q", value))
		return def
	}
	if parsed <= 0 {
		l.reject(key, fmt.Sprintf("durée strictement positive attendue, reçu %q", value))
		return def
	}
	return parsed
}

func (l *loader) environment() Environment {
	value := l.str(keyEnv, string(Development))
	switch Environment(strings.ToLower(value)) {
	case Development:
		return Development
	case Production:
		return Production
	default:
		l.reject(keyEnv, fmt.Sprintf("valeur attendue : %s ou %s, reçu %q", Development, Production, value))
		return Development
	}
}

// listenAddr valide une adresse d'écoute hôte:port. L'hôte peut rester vide
// (« :8080 » écoute sur toutes les interfaces), le port ne le peut pas.
func (l *loader) listenAddr() string {
	value := l.str(keyListenAddr, defaultListenAddr)

	_, port, err := net.SplitHostPort(value)
	if err != nil {
		l.reject(keyListenAddr, fmt.Sprintf("adresse attendue au format hôte:port, par exemple %s, reçu %q", defaultListenAddr, value))
		return defaultListenAddr
	}
	if port == "" {
		l.reject(keyListenAddr, fmt.Sprintf("le port est obligatoire, reçu %q", value))
		return defaultListenAddr
	}

	return value
}

// baseURL valide l'URL publique de l'instance. Le chemin est conservé — une
// instance peut vivre derrière un préfixe — mais sa barre oblique finale est
// retirée pour que la concaténation des liens reste prévisible.
func (l *loader) baseURL() *url.URL {
	value := l.str(keyBaseURL, defaultBaseURL)

	parsed, err := url.Parse(value)
	if err != nil {
		l.reject(keyBaseURL, fmt.Sprintf("URL illisible : %v", err))
		return mustParseURL(defaultBaseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		l.reject(keyBaseURL, fmt.Sprintf("URL absolue en http ou https attendue, par exemple %s, reçu %q", defaultBaseURL, value))
		return mustParseURL(defaultBaseURL)
	}
	if parsed.Host == "" {
		l.reject(keyBaseURL, fmt.Sprintf("l'URL doit comporter un hôte, reçu %q", value))
		return mustParseURL(defaultBaseURL)
	}

	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed
}

// databaseURL valide la chaîne de connexion PostgreSQL. La validation reste
// volontairement superficielle : pgx est seul juge de la syntaxe complète, et
// dupliquer sa grammaire ici ne ferait que créer un second point de vérité.
func (l *loader) databaseURL() string {
	value := l.required(keyDatabaseURL)
	if value == "" {
		return ""
	}

	parsed, err := url.Parse(value)
	if err != nil {
		l.reject(keyDatabaseURL, fmt.Sprintf("chaîne de connexion illisible : %v", err))
		return value
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		l.reject(keyDatabaseURL, fmt.Sprintf("schéma postgres:// ou postgresql:// attendu, reçu %q", parsed.Scheme))
	}

	return value
}

// documentsDir rend le répertoire de stockage toujours absolu, pour que le
// répertoire courant du processus cesse d'être un paramètre implicite.
func (l *loader) documentsDir() string {
	value := l.str(keyDocumentsDir, defaultDocumentsDir)

	absolute, err := filepath.Abs(value)
	if err != nil {
		l.reject(keyDocumentsDir, fmt.Sprintf("chemin inexploitable : %v", err))
		return value
	}

	return absolute
}

func (l *loader) logLevel() slog.Level {
	value := l.str(keyLogLevel, defaultLogLevel)

	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		l.reject(keyLogLevel, fmt.Sprintf("niveau attendu : debug, info, warn ou error, reçu %q", value))
		return slog.LevelInfo
	}

	return level
}

// logFormat choisit le rendu des journaux. Sans consigne explicite, le
// développement prend le format lisible et la production le format JSON, qui est
// celui qu'attendent les collecteurs de journaux.
func (l *loader) logFormat(env Environment) LogFormat {
	def := LogText
	if env == Production {
		def = LogJSON
	}

	value, ok := l.raw(keyLogFormat)
	if !ok {
		return def
	}

	switch LogFormat(strings.ToLower(value)) {
	case LogText:
		return LogText
	case LogJSON:
		return LogJSON
	default:
		l.reject(keyLogFormat, fmt.Sprintf("format attendu : %s ou %s, reçu %q", LogText, LogJSON, value))
		return def
	}
}

// mustParseURL n'est appelée que sur les constantes de ce fichier, dont
// l'invalidité serait un bug de compilation-time déguisé.
func mustParseURL(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(fmt.Sprintf("config : URL par défaut invalide %q : %v", raw, err))
	}
	return parsed
}
