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

// StorageBackend désigne l'implémentation du stockage des documents. C'est le
// levier du modèle d'extension de docs/ARCHITECTURE.md §3 : le port du domaine
// est le point d'extension, et cette variable dit à cmd/avanti laquelle de ses
// implémentations brancher.
type StorageBackend string

// Les backends de stockage reconnus.
const (
	// StorageFilesystem écrit sur le disque local, dans DocumentsDir. C'est le
	// choix par défaut, cohérent avec un déploiement self-hosted.
	StorageFilesystem StorageBackend = "filesystem"
	// StorageS3 écrit sur un objet compatible S3, décrit par les variables
	// AVANTI_S3_*.
	StorageS3 StorageBackend = "s3"
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
	// OAuthSecret est la clé HMAC du serveur d'autorisation OAuth 2.1. Elle
	// signe les codes d'autorisation et les jetons ; la perdre invalide tout ce
	// qui est en circulation, la divulguer permet d'en fabriquer.
	OAuthSecret []byte
	// DocumentsDir est le répertoire, toujours absolu, où l'adapter de stockage
	// dépose le contenu binaire des pièces du dossier. Il ne sert qu'au
	// backend filesystem.
	DocumentsDir string
	// StorageBackend choisit l'implémentation du stockage des documents :
	// filesystem (défaut) ou s3.
	StorageBackend StorageBackend
	// S3Endpoint est l'adresse du service S3, au format hôte ou hôte:port,
	// sans schéma. Obligatoire quand StorageBackend vaut s3, ignorée sinon.
	S3Endpoint string
	// S3Bucket est le seau qui reçoit les contenus. Obligatoire avec s3.
	S3Bucket string
	// S3AccessKey et S3SecretKey sont les identifiants d'accès. Obligatoires
	// avec s3. La clé secrète est un secret au même titre qu'OAuthSecret : elle
	// ne sort jamais dans les journaux, et l'identifiant d'accès non plus, par
	// prudence.
	S3AccessKey string
	S3SecretKey string
	// S3Region est la région du seau. Facultative : la plupart des S3
	// auto-hébergés n'en ont qu'une et l'ignorent.
	S3Region string
	// S3UseSSL commande le passage en HTTPS vers le service S3. Vrai par
	// défaut : les documents sont confidentiels, les faire voyager en clair
	// doit être un choix explicite de développement.
	S3UseSSL bool
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
	keyOAuthSecret      = "OAUTH_SECRET" // #nosec G101 -- nom d'une variable d'environnement, pas un secret.
	keyDocumentsDir     = "DOCUMENTS_DIR"
	keyStorageBackend   = "STORAGE_BACKEND"
	keyS3Endpoint       = "S3_ENDPOINT"
	keyS3Bucket         = "S3_BUCKET"
	keyS3AccessKey      = "S3_ACCESS_KEY" // #nosec G101 -- nom d'une variable d'environnement, pas un secret.
	keyS3SecretKey      = "S3_SECRET_KEY" // #nosec G101 -- nom d'une variable d'environnement, pas un secret.
	keyS3Region         = "S3_REGION"
	keyS3UseSSL         = "S3_USE_SSL"
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

// Contraintes sur le secret OAuth.
const (
	// minOAuthSecretLength est la longueur minimale de la clé HMAC, en octets.
	// Trente-deux est ce qu'exige la bibliothèque qui l'emploie (HMAC-SHA512/256)
	// et ce que rend `openssl rand -base64 32` — la commande donnée en exemple
	// dans .env.example produit 44 caractères, donc de la marge.
	minOAuthSecretLength = 32
	// placeholderPrefix marque les valeurs d'exemple de .env.example. Elles
	// conviennent en développement, où elles évitent d'imposer une cérémonie
	// avant le premier démarrage ; elles sont refusées en production, où laisser
	// tourner une clé publiée dans le dépôt reviendrait à n'en avoir aucune.
	placeholderPrefix = "change-me"
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
		DatabaseURL:      l.databaseURL(env),
		OAuthSecret:      l.oauthSecret(env),
		DocumentsDir:     l.documentsDir(),
		LogLevel:         l.logLevel(),
		LogFormat:        l.logFormat(env),
		MigrateOnStart:   l.boolean(keyMigrateOnStart, defaultMigrateOnStart),
		DBConnectTimeout: l.duration(keyDBConnectTimeout, defaultDBConnectTimeout),
		ShutdownTimeout:  l.duration(keyShutdownTimeout, defaultShutdownTimeout),
	}
	l.storage(cfg, env)

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
		// La clé HMAC ne sort jamais, pas même tronquée : un préfixe suffirait à
		// réduire de plusieurs ordres de grandeur le coût d'une recherche.
		slog.Int("oauth_secret_length", len(c.OAuthSecret)),
		slog.String("documents_dir", c.DocumentsDir),
		slog.String("storage_backend", string(c.StorageBackend)),
		// Du S3 ne sortent que l'adresse, le seau et le drapeau TLS — de quoi
		// diagnostiquer un branchement. La clé secrète suit la règle
		// d'OAuthSecret : jamais, pas même sa longueur ; et l'identifiant
		// d'accès non plus — il est la moitié d'une paire d'identifiants, le
		// journal n'a pas à en offrir la première moitié.
		slog.String("s3_endpoint", c.S3Endpoint),
		slog.String("s3_bucket", c.S3Bucket),
		slog.String("s3_region", c.S3Region),
		slog.Bool("s3_use_ssl", c.S3UseSSL),
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

// baseURL valide l'URL publique de l'instance. Elle doit être la racine d'un
// hôte, sans chemin : les documents de découverte de l'accès agent (RFC 8414
// et RFC 9728) sont cherchés sous /.well-known À LA RACINE de l'hôte, et une
// instance servie sous un préfixe (« https://exemple.fr/avanti ») aurait un
// serveur OAuth et un serveur MCP introuvables pour tout client conforme. La
// barre oblique finale seule est tolérée et retirée.
func (l *loader) baseURL() *url.URL {
	value := l.str(keyBaseURL, defaultBaseURL)

	parsed, err := url.Parse(value)
	if err != nil {
		l.reject(keyBaseURL, fmt.Sprintf("URL illisible : %v", err))
		return defaultBaseURLParsed()
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		l.reject(keyBaseURL, fmt.Sprintf("URL absolue en http ou https attendue, par exemple %s, reçu %q", defaultBaseURL, value))
		return defaultBaseURLParsed()
	}
	if parsed.Host == "" {
		l.reject(keyBaseURL, fmt.Sprintf("l'URL doit comporter un hôte, reçu %q", value))
		return defaultBaseURLParsed()
	}

	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""

	if parsed.Path != "" {
		l.reject(keyBaseURL, fmt.Sprintf(
			"l'URL publique doit être la racine d'un hôte, sans chemin — l'accès agent (OAuth, MCP) publie ses documents de découverte sous /.well-known à la racine ; utilisez un sous-domaine plutôt qu'un préfixe, reçu %q", value))
		return defaultBaseURLParsed()
	}

	return parsed
}

// databaseURL valide la chaîne de connexion PostgreSQL. La validation reste
// volontairement superficielle : pgx est seul juge de la syntaxe complète, et
// dupliquer sa grammaire ici ne ferait que créer un second point de vérité.
//
// Une exception, qui suit la règle d'oauthSecret : en production, le mot de
// passe d'exemple publié dans le dépôt est refusé — le laisser tourner
// reviendrait à protéger la base par un mot de passe que .env.example donne.
func (l *loader) databaseURL(env Environment) string {
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

	if password, hasPassword := parsed.User.Password(); hasPassword &&
		env == Production && strings.HasPrefix(password, placeholderPrefix) {
		l.reject(keyDatabaseURL, fmt.Sprintf(
			"le mot de passe d'exemple de .env.example est refusé en %s — donnez à PostgreSQL un mot de passe fort et propre à l'instance",
			Production))
	}

	return value
}

// oauthSecret lit et valide la clé HMAC du serveur d'autorisation.
//
// La valeur est prise telle quelle, sans décodage : ce qui compte pour un HMAC
// est la quantité d'aléa des octets fournis, et exiger du base64 imposerait un
// format sans rien ajouter à la solidité. `openssl rand -base64 32` reste la
// façon recommandée d'en obtenir une, parce qu'elle est sûre et qu'elle tient
// sur une ligne de fichier d'environnement.
//
// Deux refus, et un seul est une question de longueur :
//
//   - moins de [minOAuthSecretLength] octets, la bibliothèque de signature
//     refuserait la clé à l'exécution. Mieux vaut le dire au démarrage, avec le
//     nom de la variable, que lors de la première tentative de connexion d'un
//     agent ;
//   - la valeur d'exemple en production. Elle est publiée dans le dépôt : la
//     garder reviendrait à laisser quiconque a lu .env.example fabriquer des
//     jetons valides pour l'instance.
func (l *loader) oauthSecret(env Environment) []byte {
	value := l.required(keyOAuthSecret)
	if value == "" {
		return nil
	}

	if len(value) < minOAuthSecretLength {
		l.reject(keyOAuthSecret, fmt.Sprintf(
			"clé de %d octets, %d au minimum — engendrez-la avec `openssl rand -base64 32`",
			len(value), minOAuthSecretLength))
		return nil
	}
	if env == Production && strings.HasPrefix(value, placeholderPrefix) {
		l.reject(keyOAuthSecret, fmt.Sprintf(
			"la valeur d'exemple de .env.example est refusée en %s — engendrez-la avec `openssl rand -base64 32`",
			Production))
		return nil
	}

	return []byte(value)
}

// storage lit le choix du backend de stockage des documents, puis les
// variables S3 quand — et seulement quand — c'est lui qui est choisi. Les
// variables S3 d'un backend filesystem sont ignorées, pas validées : une
// ligne commentée à moitié dans un .env ne doit pas empêcher un démarrage qui
// ne s'en sert pas.
//
// En production, les identifiants S3 d'exemple (préfixe change-me, publiés
// dans .env.example) sont refusés, par la même règle que pour oauthSecret :
// des documents d'assurance derrière une clé publiée ne sont derrière rien.
func (l *loader) storage(cfg *Config, env Environment) {
	cfg.StorageBackend = l.storageBackend()
	if cfg.StorageBackend != StorageS3 {
		return
	}

	cfg.S3Endpoint = l.requiredForS3(keyS3Endpoint)
	cfg.S3Bucket = l.requiredForS3(keyS3Bucket)
	cfg.S3AccessKey = l.s3Credential(keyS3AccessKey, env)
	cfg.S3SecretKey = l.s3Credential(keyS3SecretKey, env)
	cfg.S3Region = l.str(keyS3Region, "")
	cfg.S3UseSSL = l.boolean(keyS3UseSSL, true)
}

// storageBackend valide le choix du backend.
func (l *loader) storageBackend() StorageBackend {
	value := l.str(keyStorageBackend, string(StorageFilesystem))
	switch StorageBackend(strings.ToLower(value)) {
	case StorageFilesystem:
		return StorageFilesystem
	case StorageS3:
		return StorageS3
	default:
		l.reject(keyStorageBackend, fmt.Sprintf("valeur attendue : %s ou %s, reçu %q", StorageFilesystem, StorageS3, value))
		return StorageFilesystem
	}
}

// requiredForS3 exige une variable, avec un message qui dit pourquoi elle
// l'est devenue : c'est le choix du backend qui la rend obligatoire, pas la
// variable elle-même.
func (l *loader) requiredForS3(key string) string {
	value, ok := l.raw(key)
	if !ok {
		l.reject(key, fmt.Sprintf("variable obligatoire quand %s%s vaut %s", Prefix, keyStorageBackend, StorageS3))
	}
	return value
}

// s3Credential exige un identifiant S3 et, en production, refuse la valeur
// d'exemple publiée dans le dépôt.
func (l *loader) s3Credential(key string, env Environment) string {
	value := l.requiredForS3(key)

	if env == Production && strings.HasPrefix(value, placeholderPrefix) {
		l.reject(key, fmt.Sprintf(
			"la valeur d'exemple de .env.example est refusée en %s — utilisez les identifiants réels de votre service S3",
			Production))
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

// defaultBaseURLParsed rend l'URL publique par défaut analysée. Elle ne sert
// que de repli quand la valeur fournie est rejetée — le chargement échoue de
// toute façon, mais la structure rendue reste utilisable par les messages
// d'erreur. L'invalidité de la constante serait un bug de compilation déguisé.
func defaultBaseURLParsed() *url.URL {
	parsed, err := url.Parse(defaultBaseURL)
	if err != nil {
		panic(fmt.Sprintf("config : URL par défaut invalide %q : %v", defaultBaseURL, err))
	}
	return parsed
}
