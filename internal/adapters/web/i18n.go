package web

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// localesFS embarque les catalogues de traduction. Le français est aujourd'hui
// la seule langue fournie, mais toute chaîne affichée passe déjà par le
// catalogue : ajouter une langue reviendra à déposer un fichier ici, sans
// toucher au moindre gabarit.
//
//go:embed locales/*.json
var localesFS embed.FS

// DefaultLanguage est la langue de repli, et pour l'instant la seule.
var DefaultLanguage = language.French

// Catalog porte les messages traduits de toutes les langues chargées.
type Catalog struct {
	bundle *i18n.Bundle
}

// NewCatalog charge les catalogues embarqués.
func NewCatalog() (*Catalog, error) {
	bundle := i18n.NewBundle(DefaultLanguage)

	files, err := fs.Glob(localesFS, "locales/*.json")
	if err != nil {
		return nil, fmt.Errorf("lecture des catalogues de traduction : %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("aucun catalogue de traduction embarqué")
	}

	for _, file := range files {
		if _, err := bundle.LoadMessageFileFS(localesFS, file); err != nil {
			return nil, fmt.Errorf("chargement du catalogue %s : %w", path.Base(file), err)
		}
	}

	return &Catalog{bundle: bundle}, nil
}

// Translator renvoie le traducteur correspondant aux langues demandées, dans
// l'ordre de préférence — typiquement le contenu d'un en-tête Accept-Language.
// Une langue inconnue retombe sur DefaultLanguage.
func (c *Catalog) Translator(preferred ...string) *Translator {
	return &Translator{
		localizer: i18n.NewLocalizer(c.bundle, preferred...),
		lang:      resolveLang(preferred),
	}
}

// resolveLang retient la première langue reconnue, pour l'attribut lang de la
// balise html. Le rendu, lui, reste l'affaire du localizer.
func resolveLang(preferred []string) string {
	for _, candidate := range preferred {
		tag, err := language.Parse(strings.TrimSpace(candidate))
		if err == nil && !tag.IsRoot() {
			return tag.String()
		}
	}
	return DefaultLanguage.String()
}

// Translator rend les messages dans une langue donnée. Il est bon marché à
// construire : un par requête est le mode d'emploi normal.
type Translator struct {
	localizer *i18n.Localizer
	lang      string
}

// Lang renvoie l'étiquette de langue retenue, destinée à l'attribut lang du
// document HTML.
func (t *Translator) Lang() string {
	return t.lang
}

// T traduit le message id. Les arguments qui suivent sont des couples clé,
// valeur alimentant les substitutions du message ({{.Version}} par exemple).
//
// Un message absent ou mal appelé rend un marqueur voyant plutôt qu'une chaîne
// vide : une traduction manquante doit sauter aux yeux à la première page
// affichée, pas se cacher dans un trou du gabarit.
func (t *Translator) T(id string, pairs ...string) string {
	data, err := templateData(pairs)
	if err != nil {
		return marker(id)
	}

	rendered, err := t.localizer.Localize(&i18n.LocalizeConfig{
		MessageID:    id,
		TemplateData: data,
	})
	if err != nil {
		return marker(id)
	}

	return rendered
}

// templateData transforme une suite clé, valeur, clé, valeur… en la carte
// qu'attend go-i18n. Un nombre impair d'arguments est une faute d'appel.
func templateData(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("substitutions de traduction : %d arguments, un nombre pair est attendu", len(pairs))
	}

	data := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		data[pairs[i]] = pairs[i+1]
	}

	return data, nil
}

// marker signale une traduction manquante de façon indubitable.
func marker(id string) string {
	return "!" + id + "!"
}
