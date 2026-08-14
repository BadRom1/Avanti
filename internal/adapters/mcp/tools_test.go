package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Romain-Badino/Avanti/internal/planning"
)

// decodeStructured relit la sortie structurée d'un tool dans la forme donnée.
func decodeStructured(t *testing.T, result *sdk.CallToolResult, out any) {
	t.Helper()

	if result.IsError {
		t.Fatalf("le tool a rendu une erreur : %s", resultText(result))
	}

	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("sortie structurée non sérialisable : %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("sortie structurée illisible : %v — %s", err, raw)
	}
}

// TestToolsListed vérifie que la session s'ouvre avec un jeton de propriétaire
// et que les tools annoncés portent des noms français.
func TestToolsListed(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	session := tb.connect(t, tokenOwner)

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list : %v", err)
	}

	names := make(map[string]bool, len(tools.Tools))
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}

	for _, want := range []string{
		"devis_liste", "devis_detail", "devis_enregistrer",
		"finances_synthese", "finances_factures", "finances_acomptes",
		"facture_enregistrer", "acompte_enregistrer",
		"planning_etapes", "planning_jalons", "etape_demarrer", "etape_terminer",
		"documents_liste", "assurance_preparer_envoi",
	} {
		if !names[want] {
			t.Errorf("tool %q absent de tools/list", want)
		}
	}
}

// TestToolRefusedWithoutDomainScope vérifie le refus par scope de domaine : un
// jeton qui porte mcp mais pas devis:read reçoit une erreur qui NOMME le scope,
// jamais un résultat vide.
//
// L'acteur est FORGÉ (identity.NewActorWithScopes) : aucun rôle réel ne porte
// mcp sans les scopes de domaine — le propriétaire a tout, le collaborateur n'a
// pas mcp — et un jeton réel ne peut donc pas produire ce cas aujourd'hui. Le
// forger reste nécessaire : le consentement OAuth permet de n'accorder qu'une
// partie des scopes demandés, et ce refus est ce qu'un agent verrait alors.
func TestToolRefusedWithoutDomainScope(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	tb.seedComparaison(t)

	session := tb.connect(t, tokenMCPOnly)

	result := callTool(t, session, "devis_liste", nil)

	if !result.IsError {
		t.Fatal("le tool a répondu à un jeton sans devis:read")
	}
	if text := resultText(result); !strings.Contains(text, "devis:read") {
		t.Errorf("le refus ne nomme pas le scope manquant : %q", text)
	}
}

// TestDevisListeEtEnregistrer joue une consultation puis une écriture avec un
// jeton de propriétaire : la liste rend le semé, l'écriture atteint le domaine
// avec l'identifiant du compte en acteur.
func TestDevisListeEtEnregistrer(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	demande, seeded := tb.seedComparaison(t)

	session := tb.connect(t, tokenOwner)

	var liste struct {
		Comparaisons []struct {
			Demande struct {
				ID  string `json:"id"`
				Lot string `json:"lot"`
			} `json:"demande"`
			Devis []struct {
				ID              string `json:"id"`
				Entreprise      string `json:"entreprise"`
				MontantCentimes int64  `json:"montant_centimes"`
				Statut          string `json:"statut"`
			} `json:"devis"`
		} `json:"comparaisons"`
	}
	decodeStructured(t, callTool(t, session, "devis_liste", nil), &liste)

	if len(liste.Comparaisons) != 1 {
		t.Fatalf("comparaisons = %d, attendu 1", len(liste.Comparaisons))
	}
	comparaison := liste.Comparaisons[0]
	if comparaison.Demande.Lot != "Charpente" || comparaison.Demande.ID != demande.ID.String() {
		t.Errorf("demande = %+v, attendu le lot Charpente semé", comparaison.Demande)
	}
	if len(comparaison.Devis) != 1 || comparaison.Devis[0].MontantCentimes != int64(seeded.Montant) {
		t.Errorf("devis = %+v, attendu le devis semé à %d centimes", comparaison.Devis, seeded.Montant)
	}

	// Écriture : un second devis sur la même demande.
	var enregistre struct {
		ID     string `json:"id"`
		Statut string `json:"statut"`
	}
	decodeStructured(t, callTool(t, session, "devis_enregistrer", map[string]any{
		"demande_id":       demande.ID.String(),
		"entreprise":       "Toitures Réunies",
		"montant_centimes": 1_050_000,
		"recu_le":          "2026-05-12",
	}), &enregistre)

	if enregistre.Statut != "recu" {
		t.Errorf("statut = %q, attendu recu", enregistre.Statut)
	}

	stored, ok := tb.devis.devisParEntreprise("Toitures Réunies")
	if !ok {
		t.Fatal("le devis enregistré par MCP n'est pas dans le dépôt")
	}
	// La traçabilité : l'acteur du jeton signe l'écriture.
	if string(stored.RecordedBy) != testOwnerUserID {
		t.Errorf("RecordedBy = %q, attendu %q", stored.RecordedBy, testOwnerUserID)
	}
}

// TestDevisEnregistrerRefuseValiditeAbsurde vérifie la borne de validite_jours :
// une valeur qui ferait déborder la conversion en durée est refusée avec un
// message français, avant tout calcul.
func TestDevisEnregistrerRefuseValiditeAbsurde(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	demande, _ := tb.seedComparaison(t)

	session := tb.connect(t, tokenOwner)

	result := callTool(t, session, "devis_enregistrer", map[string]any{
		"demande_id":       demande.ID.String(),
		"entreprise":       "Toitures Réunies",
		"montant_centimes": 1_000_000,
		"recu_le":          "2026-05-12",
		"validite_jours":   999_999_999,
	})

	if !result.IsError {
		t.Fatal("une durée de validité absurde a été acceptée")
	}
	if text := resultText(result); !strings.Contains(text, "validité") {
		t.Errorf("le refus ne nomme pas la validité : %q", text)
	}
}

// TestEtapeDemarrer vérifie l'invariant du planning au travers du tool : pas de
// démarrage avant les prérequis, et le refus est le message français du
// domaine. Le tool relit lui-même l'étape pour la garde optimiste — un agent
// n'a pas de formulaire à état.
func TestEtapeDemarrer(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	prerequis := tb.seedEtape(t, "Gros œuvre")
	dependante := tb.seedEtape(t, "Charpente", prerequis.ID)

	session := tb.connect(t, tokenOwner)

	// Refus : le prérequis n'est pas terminé.
	blocked := callTool(t, session, "etape_demarrer", map[string]any{
		"etape_id": dependante.ID.String(),
	})
	if !blocked.IsError {
		t.Fatal("l'étape dépendante a démarré avant son prérequis")
	}
	if text := resultText(blocked); !strings.Contains(text, "prérequis") {
		t.Errorf("le refus n'explique pas les prérequis : %q", text)
	}

	// Le prérequis, lui, démarre.
	var started struct {
		Statut string `json:"statut"`
	}
	decodeStructured(t, callTool(t, session, "etape_demarrer", map[string]any{
		"etape_id": prerequis.ID.String(),
	}), &started)
	if started.Statut != planning.StatutEnCours.String() {
		t.Errorf("statut = %q, attendu en_cours", started.Statut)
	}

	// Et se termine.
	var finished struct {
		Statut string `json:"statut"`
	}
	decodeStructured(t, callTool(t, session, "etape_terminer", map[string]any{
		"etape_id": prerequis.ID.String(),
	}), &finished)
	if finished.Statut != planning.StatutTerminee.String() {
		t.Errorf("statut = %q, attendu terminee", finished.Statut)
	}
}

// TestAssurancePreparerEnvoi vérifie l'assemblage transverse et surtout ce que
// le tool NE fait PAS : le dossier porte l'avertissement en toutes lettres, et
// rien n'est envoyé nulle part — cet adapter n'a aucun port d'envoi.
func TestAssurancePreparerEnvoi(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	session := tb.connect(t, tokenOwner)

	// Une facture par le tool lui-même : hors devis, le cas le plus simple.
	facture := callTool(t, session, "facture_enregistrer", map[string]any{
		"entreprise":       "Matériaux du Centre",
		"montant_centimes": 25_000,
		"date":             "2026-06-01",
	})
	if facture.IsError {
		t.Fatalf("facture_enregistrer refusé : %s", resultText(facture))
	}

	var dossier struct {
		Avertissement string `json:"avertissement"`
		Factures      []struct {
			Facture struct {
				Entreprise string `json:"entreprise"`
			} `json:"facture"`
		} `json:"factures"`
		Totaux struct {
			FactureCentimes int64 `json:"facture_centimes"`
		} `json:"totaux"`
	}
	decodeStructured(t, callTool(t, session, "assurance_preparer_envoi", nil), &dossier)

	if !strings.Contains(dossier.Avertissement, "aucun envoi") {
		t.Errorf("avertissement = %q, il doit dire qu'aucun envoi n'est effectué", dossier.Avertissement)
	}
	if len(dossier.Factures) != 1 || dossier.Factures[0].Facture.Entreprise != "Matériaux du Centre" {
		t.Errorf("factures du dossier = %+v, attendu la facture enregistrée", dossier.Factures)
	}
	if dossier.Totaux.FactureCentimes != 25_000 {
		t.Errorf("total facturé = %d, attendu 25000", dossier.Totaux.FactureCentimes)
	}
}

// TestFactureEnregistrerRefuseDevisNonRetenu vérifie la résolution transverse :
// rattacher une facture à un devis encore en comparaison est refusé avec un
// message explicable, comme sur le web.
func TestFactureEnregistrerRefuseDevisNonRetenu(t *testing.T) {
	t.Parallel()

	tb := newTestbed(t)
	_, proposition := tb.seedComparaison(t) // statut « recu », pas retenu

	session := tb.connect(t, tokenOwner)

	result := callTool(t, session, "facture_enregistrer", map[string]any{
		"devis_id":         proposition.ID.String(),
		"entreprise":       "Charpentes du Val",
		"montant_centimes": 100_000,
		"date":             "2026-06-01",
	})

	if !result.IsError {
		t.Fatal("une facture a été rattachée à un devis non retenu")
	}
	if text := resultText(result); !strings.Contains(text, "retenu") {
		t.Errorf("le refus n'explique pas le statut du devis : %q", text)
	}
}
