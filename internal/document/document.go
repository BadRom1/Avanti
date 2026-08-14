package document

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Bornes du domaine.
const (
	// MaxFileSize est la taille maximale du contenu d'une pièce, en octets :
	// 25 Mio. Assez pour un scan de devis multi-pages ou une photo de chantier
	// en pleine résolution ; au-delà, c'est presque toujours un fichier à
	// recompresser, et la borne protège le stockage comme la mémoire du
	// serveur. Elle est exportée : l'adapter web s'en sert pour dimensionner sa
	// propre limite de requête.
	MaxFileSize int64 = 25 << 20

	// maxFileNameLength borne le nom de fichier, en caractères, après
	// nettoyage. 255 est la limite des systèmes de fichiers courants : le nom
	// d'origine doit pouvoir redevenir un nom de fichier au téléchargement.
	maxFileNameLength = 255

	// maxDescriptionLength borne la description libre.
	maxDescriptionLength = 2000

	// maxTargetIDLength borne l'identifiant d'une cible de rattachement. Les
	// identifiants réels sont des UUID de 36 caractères ; la borne n'est pas
	// là pour eux mais pour qu'un POST forgé ne stocke pas un roman dans une
	// colonne de référence.
	maxTargetIDLength = 255
)

// allowedMimeTypes est la liste fermée des types de contenu acceptés.
//
// C'est une liste d'autorisation, pas d'interdiction, pour la même raison que
// les allow-lists depguard : un format oublié est refusé bruyamment, pas admis
// en silence. PDF pour les documents, JPEG/PNG/WebP pour les photos — le
// dossier d'un chantier n'a besoin de rien d'autre, et chaque format ajouté
// serait une surface d'attaque de plus dans le navigateur qui l'ouvrira.
var allowedMimeTypes = []string{
	"application/pdf",
	"image/jpeg",
	"image/png",
	"image/webp",
}

// AllowedMimeTypes renvoie les types de contenu acceptés, dans un ordre stable.
//
// La tranche renvoyée est une copie : la modifier ne change rien au domaine.
func AllowedMimeTypes() []string {
	return slices.Clone(allowedMimeTypes)
}

// Category est le classement d'une pièce dans le dossier.
//
// Les valeurs sont en français parce qu'elles sont stockées telles quelles en
// base et visibles telles quelles : c'est le même vocabulaire des deux côtés,
// et la correspondance se lit sans table de traduction.
type Category string

// Les catégories reconnues.
const (
	// CategoryDevisSigne est le devis signé, celui qui engage.
	CategoryDevisSigne Category = "devis_signe"
	// CategoryFacture est une facture reçue, le plus souvent scannée.
	CategoryFacture Category = "facture"
	// CategoryPhotoChantier est une photo de l'avancement des travaux.
	CategoryPhotoChantier Category = "photo_chantier"
	// CategoryRapportExpertise est un rapport d'expert — sinistre, structure.
	CategoryRapportExpertise Category = "rapport_expertise"
	// CategoryCourrierAssurance est un échange avec l'assurance.
	CategoryCourrierAssurance Category = "courrier_assurance"
	// CategoryAutre recueille ce que les autres catégories ne décrivent pas.
	CategoryAutre Category = "autre"
)

// allCategories énumère les catégories dans l'ordre où elles sont présentées à
// l'utilisateur. C'est aussi la référence de [Category.Known] : une catégorie
// absente de cette liste n'existe pas.
var allCategories = []Category{
	CategoryDevisSigne,
	CategoryFacture,
	CategoryPhotoChantier,
	CategoryRapportExpertise,
	CategoryCourrierAssurance,
	CategoryAutre,
}

// AllCategories renvoie les catégories reconnues, dans un ordre stable.
//
// La tranche renvoyée est une copie : la modifier ne change rien au domaine.
func AllCategories() []Category {
	return slices.Clone(allCategories)
}

// Known indique si la catégorie fait partie de celles que le domaine reconnaît.
func (c Category) Known() bool {
	return slices.Contains(allCategories, c)
}

// String rend la catégorie telle qu'elle est stockée.
func (c Category) String() string {
	return string(c)
}

// NormalizeCategory met une saisie de catégorie sous sa forme canonique et
// refuse ce que le domaine ne reconnaît pas.
func NormalizeCategory(raw string) (Category, error) {
	category := Category(strings.ToLower(strings.TrimSpace(raw)))
	if !category.Known() {
		return "", fmt.Errorf("%w : %q", ErrUnknownCategory, raw)
	}

	return category, nil
}

// TargetType est la nature de ce qu'une pièce justifie.
//
// Comme les catégories, les valeurs sont en français : stockées en base,
// visibles dans les formulaires.
type TargetType string

// Les types de cible reconnus.
const (
	// TargetDevis rattache la pièce à un devis reçu.
	TargetDevis TargetType = "devis"
	// TargetFacture rattache la pièce à une facture du domaine finance.
	TargetFacture TargetType = "facture"
	// TargetEtape rattache la pièce à une étape du domaine planning.
	TargetEtape TargetType = "etape"
)

// allTargetTypes est la référence de [TargetType.Known].
var allTargetTypes = []TargetType{TargetDevis, TargetFacture, TargetEtape}

// Known indique si le type de cible fait partie de ceux que le domaine
// reconnaît.
func (t TargetType) Known() bool {
	return slices.Contains(allTargetTypes, t)
}

// String rend le type de cible tel qu'il est stocké.
func (t TargetType) String() string {
	return string(t)
}

// Target est le rattachement d'une pièce à ce qu'elle justifie : un devis, une
// facture, une étape.
//
// C'est une référence faible au sens de R2 de docs/ARCHITECTURE.md :
// l'identifiant est une simple chaîne, jamais le type du domaine visé — le
// domaine document n'importe ni devis, ni finance, ni planning, et n'a aucun
// moyen de savoir si la cible existe encore. Une cible disparue laisse une
// référence morte, que l'interface traite comme telle.
//
// La valeur zéro est significative : elle dit « pièce non rattachée », et le
// rattachement est optionnel — une photo de chantier ne justifie rien de
// précis.
type Target struct {
	// Type dit la nature de la cible.
	Type TargetType
	// ID est l'identifiant de la cible, transporté en simple chaîne.
	ID string
}

// Zero dit que la pièce n'est rattachée à rien.
func (t Target) Zero() bool {
	return t.Type == "" && t.ID == ""
}

// NormalizeTarget nettoie un rattachement et refuse les formes incohérentes :
// les deux champs sont vides — pièce libre — ou les deux sont remplis avec un
// type connu. Un type sans identifiant, ou l'inverse, est une saisie
// retournée, pas un rattachement partiel.
func NormalizeTarget(raw Target) (Target, error) {
	target := Target{
		Type: TargetType(strings.ToLower(strings.TrimSpace(string(raw.Type)))),
		ID:   strings.TrimSpace(raw.ID),
	}

	if target.Zero() {
		return Target{}, nil
	}
	if target.Type == "" || target.ID == "" {
		return Target{}, fmt.Errorf("%w : le type et l'identifiant de la cible vont ensemble", ErrInvalidTarget)
	}
	if !target.Type.Known() {
		return Target{}, fmt.Errorf("%w : type de cible %q", ErrInvalidTarget, raw.Type)
	}
	if utf8.RuneCountInString(target.ID) > maxTargetIDLength {
		return Target{}, fmt.Errorf("%w : identifiant de cible de plus de %d caractères", ErrInvalidTarget, maxTargetIDLength)
	}

	return target, nil
}

// Document est une pièce du dossier : ses métadonnées, jamais son contenu.
//
// Le contenu binaire ne passe pas par le domaine — c'est la décision
// structurante de docs/ARCHITECTURE.md §4 : il est confié à un [Storage] sous
// la clé de l'identifiant, et le domaine ne manipule que ce qui se compare, se
// classe et s'affiche.
type Document struct {
	// ID identifie la pièce. C'est aussi sa clé de stockage.
	ID ID
	// FileName est le nom de fichier d'origine, nettoyé : sans chemin, sans
	// caractère de contrôle. C'est le nom que le téléchargement rendra.
	FileName string
	// MimeType est le type de contenu, dans la liste [AllowedMimeTypes]. C'est
	// le type constaté par l'adapter au dépôt, pas celui que le client annonce.
	MimeType string
	// SizeBytes est la taille du contenu, en octets. Toujours strictement
	// positive et bornée par [MaxFileSize].
	SizeBytes int64
	// Category classe la pièce dans le dossier.
	Category Category
	// Description précise ce que la pièce ne dit pas d'elle-même. Facultative.
	Description string
	// Target rattache la pièce à ce qu'elle justifie. La valeur zéro vaut
	// « pièce libre ».
	Target Target
	// UploadedBy est l'acteur qui a déposé la pièce.
	UploadedBy ActeurID
	// CreatedAt est la date du dépôt dans Avanti.
	CreatedAt time.Time
	// UpdatedAt est la date de la dernière modification.
	UpdatedAt time.Time
}

// NormalizeFileName met un nom de fichier sous sa forme canonique et refuse ce
// qui n'en est pas un.
//
// Le nettoyage retire tout ce qu'un client peut glisser dans le champ et qui
// n'a rien à faire dans un nom : le chemin — seul le dernier segment est
// gardé, quel que soit le séparateur — et les caractères de contrôle, dont les
// retours à la ligne qu'un en-tête Content-Disposition ne doit jamais
// recevoir. Ce qui reste doit être un nom : non vide, et assez court pour
// redevenir un fichier au téléchargement.
func NormalizeFileName(raw string) (string, error) {
	name := raw

	// Le dernier segment du chemin, pour les deux familles de séparateurs : un
	// navigateur Windows peut envoyer « C:\Dossier\devis.pdf ».
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}

	// Partent aussi les caractères de *format* Unicode (catégorie Cf) : U+202E
	// (renversement droite-à-gauche) ferait afficher « fdp.gpj » comme
	// « jpg.pdf » — une usurpation visuelle d'extension — et les largeurs
	// nulles cachent ce qu'un œil doit voir.
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)

	// « . » et « .. » sont des chemins déguisés en noms, pas des noms.
	if name == "" || name == "." || name == ".." {
		return "", ErrEmptyFileName
	}
	if utf8.RuneCountInString(name) > maxFileNameLength {
		return "", fmt.Errorf("%w : plus de %d caractères", ErrFileNameTooLong, maxFileNameLength)
	}

	return name, nil
}

// normalizeDescription borne la description sans en changer la mise en forme :
// les retours à la ligne font partie de ce qui a été saisi. Seuls les blancs
// de bordure partent.
func normalizeDescription(raw string) (string, error) {
	description := strings.TrimSpace(raw)

	if utf8.RuneCountInString(description) > maxDescriptionLength {
		return "", fmt.Errorf("%w : plus de %d caractères", ErrDescriptionTooLong, maxDescriptionLength)
	}

	return description, nil
}

// normalizeMimeType vérifie qu'un type de contenu est dans la liste
// d'autorisation. Aucune tolérance de forme au-delà de la casse et des blancs
// de bordure : les paramètres (« ; charset=... ») sont l'affaire de
// l'appelant, le domaine ne compare que des types nus.
func normalizeMimeType(raw string) (string, error) {
	mimeType := strings.ToLower(strings.TrimSpace(raw))

	if !slices.Contains(allowedMimeTypes, mimeType) {
		return "", fmt.Errorf("%w : %q", ErrUnsupportedMimeType, raw)
	}

	return mimeType, nil
}
