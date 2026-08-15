package mcp_test

import (
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// refusScopeManquant est le fragment du refus que rend la garde par scope de
// l'adapter (requireScope). Le vérifier — plutôt que le seul drapeau d'erreur —
// est ce qui empêche le test ci-dessous de passer pour une mauvaise raison : un
// tool sans garde répond souvent par un refus lui aussi (identifiant inconnu,
// date illisible), et ce refus-là ne prouve rien sur l'autorisation.
const refusScopeManquant = "requis : le jeton ne porte pas ce droit"

// toolsSansScopeDeDomaine énumère, par nom, les tools qui n'exigeraient
// légitimement rien de plus que le scope mcp du transport, avec la raison de
// l'exemption. Elle est VIDE, et c'est la règle : chaque tool d'Avanti touche à
// un domaine, donc en demande le scope. Elle existe pour qu'une exception
// future s'écrive ici, motivée et visible en revue, plutôt qu'en désactivant la
// vérification.
var toolsSansScopeDeDomaine = map[string]string{}

// TestChaqueToolExigeUnScopeDeDomaine parcourt les tools ANNONCÉS par le
// serveur et vérifie que chacun refuse un jeton qui ne porte que le scope mcp.
//
// La liste des tools vient de tools/list, jamais d'une énumération recopiée
// ici : un quinzième tool enregistré sans sa ligne requireScope compilerait,
// passerait le reste de la suite — la garde n'est pas structurelle, c'est la
// première ligne de chaque handler — et serait appelable par n'importe quel
// jeton d'agent. Piloter le test par l'annonce du serveur le fait échouer au
// premier oubli, sans que personne ait à penser à l'étendre.
//
// Le middleware HTTP, lui, n'exige que le scope mcp : ce qui est vérifié ici
// est bien la garde de chaque tool, la seule qui distingue les domaines.
func TestChaqueToolExigeUnScopeDeDomaine(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	// Le chantier est semé : un tool sans garde rendrait alors de vraies
	// données du dossier, pas une liste vide qui pourrait passer pour un refus.
	tb.seedComparaison(t)
	tb.seedEtape(t, "Fondations")

	session := tb.connect(t, tokenMCPOnly)

	var annonces int
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("tools/list : %v", err)
		}
		annonces++

		t.Run(tool.Name, func(t *testing.T) {
			if raison, exempte := toolsSansScopeDeDomaine[tool.Name]; exempte {
				t.Skipf("exemption assumée : %s", raison)
			}

			result := callTool(t, session, tool.Name, sampleArguments(t, tool))

			if !result.IsError {
				t.Fatalf("le tool a répondu à un jeton porteur du seul scope mcp")
			}
			if text := resultText(result); !strings.Contains(text, refusScopeManquant) {
				t.Errorf("le refus ne vient pas de la garde par scope : %q", text)
			}
		})
	}

	if annonces == 0 {
		t.Fatal("aucun tool annoncé : le test ne vérifierait rien")
	}
}

// sampleArguments fabrique les arguments obligatoires d'un tool à partir du
// schéma qu'il annonce.
//
// Ils sont nécessaires parce que le SDK valide l'entrée AVANT d'entrer dans le
// handler : un appel amputé d'un argument obligatoire n'atteindrait jamais la
// garde, et le test passerait sur une erreur de validation. Les valeurs sont
// dérivées du schéma plutôt qu'écrites à la main, de sorte qu'un tool ajouté
// demain soit couvert sans retoucher ce fichier ; elles n'ont pas à être
// sensées, la garde tranchant avant toute lecture des domaines.
func sampleArguments(t *testing.T, tool *sdk.Tool) map[string]any {
	t.Helper()

	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("schéma d'entrée inattendu : %T", tool.InputSchema)
	}

	required, ok := schema["required"].([]any)
	if !ok {
		// Aucun argument obligatoire : le tool s'appelle sans rien.
		return nil
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("des arguments obligatoires, mais aucune propriété déclarée")
	}

	args := make(map[string]any, len(required))
	for _, raw := range required {
		name, ok := raw.(string)
		if !ok {
			t.Fatalf("nom d'argument obligatoire illisible : %T", raw)
		}
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("argument obligatoire %q sans schéma", name)
		}
		args[name] = sampleValue(t, name, property)
	}

	return args
}

// sampleValue rend une valeur du type déclaré par le schéma. Un type dont ce
// test ne sait pas fabriquer d'exemple l'arrête net au lieu d'être contourné :
// c'est le signe qu'un nouveau tool attend une entrée d'une forme inédite, et
// sa garde ne doit pas cesser d'être vérifiée pour autant.
func sampleValue(t *testing.T, field string, property map[string]any) any {
	t.Helper()

	kind, ok := property["type"].(string)
	if !ok {
		t.Fatalf("argument %q : type absent du schéma", field)
	}

	switch kind {
	case "string":
		return "x"
	case "integer", "number":
		return 1
	case "boolean":
		return false
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	default:
		t.Fatalf("argument %q : type %q sans valeur d'exemple — compléter sampleValue", field, kind)
		return nil
	}
}
