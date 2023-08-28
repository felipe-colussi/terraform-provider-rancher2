package rancher2

import (
	"context"
	"log"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	managementClient "github.com/rancher/rancher/pkg/client/generated/management/v3"
)

func resourceRancher2MultiClusterApp() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceRancher2MultiClusterAppCreate,
		ReadContext:   resourceRancher2MultiClusterAppRead,
		UpdateContext: resourceRancher2MultiClusterAppUpdate,
		DeleteContext: resourceRancher2MultiClusterAppDelete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceRancher2MultiClusterAppImport,
		},

		Schema: multiClusterAppFields(),
		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Update: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
	}
}

func resourceRancher2MultiClusterAppCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	name := d.Get("name").(string)

	diagnostics := resourceRancher2AppGetVersion(ctx, d, meta)
	if diagnostics.HasError() {
		return diagnostics
	}

	multiClusterApp, err := expandMultiClusterApp(d)
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[INFO] Creating multi cluster app %s", name)

	client, err := meta.(*Config).ManagementClient()
	if err != nil {
		return diag.FromErr(err)
	}

	newMultiClusterApp, err := client.MultiClusterApp.Create(multiClusterApp)
	if err != nil {
		return diag.FromErr(err)
	}

	d.SetId(newMultiClusterApp.ID)

	if d.Get("wait").(bool) {
		stateConf := &retry.StateChangeConf{
			Pending:    []string{},
			Target:     []string{"active"},
			Refresh:    multiClusterAppStateRefreshFunc(client, newMultiClusterApp.ID),
			Timeout:    d.Timeout(schema.TimeoutCreate),
			Delay:      1 * time.Second,
			MinTimeout: 3 * time.Second,
		}
		_, waitErr := stateConf.WaitForStateContext(ctx)
		if waitErr != nil {
			return diag.Errorf("[ERROR] waiting for multi cluster app (%s) to be created: %s", newMultiClusterApp.ID, waitErr)
		}
	}

	return resourceRancher2MultiClusterAppRead(ctx, d, meta)
}

func resourceRancher2MultiClusterAppRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	id := d.Id()

	log.Printf("[INFO] Refreshing multi cluster app ID %s", id)

	client, err := meta.(*Config).ManagementClient()
	if err != nil {
		return diag.FromErr(err)
	}

	multiClusterApp, err := client.MultiClusterApp.ByID(id)
	if err != nil {
		if IsNotFound(err) || IsForbidden(err) {
			log.Printf("[INFO] multi cluster app ID %s not found.", id)
			d.SetId("")
			return nil
		}
		return diag.FromErr(err)
	}

	templateVersion, err := client.TemplateVersion.ByID(multiClusterApp.TemplateVersionID)
	if err != nil {
		return diag.FromErr(err)
	}

	return diag.FromErr(flattenMultiClusterApp(d, multiClusterApp, templateVersion.ExternalID))
}

func resourceRancher2MultiClusterAppUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	id := d.Id()

	client, err := meta.(*Config).ManagementClient()
	if err != nil {
		return diag.FromErr(err)
	}

	multiClusterApp, err := client.MultiClusterApp.ByID(id)
	if err != nil {
		return diag.FromErr(err)
	}

	updateApp := true

	// Rollback or modify targets
	if d.HasChange("revision_id") {
		updateApp = false
		revID := d.Get("revision_id").(string)
		log.Printf("[INFO] Rollbacking multi cluster app ID %s to %s", id, revID)

		rollback := &managementClient.MultiClusterAppRollbackInput{
			RevisionID: revID,
		}
		err = client.MultiClusterApp.ActionRollback(multiClusterApp, rollback)
		if err != nil {
			return diag.FromErr(err)
		}
	} else if d.HasChange("targets") {
		updateApp = false

		removeTarget := multiClusterAppTargetToRemove(d, multiClusterApp)
		addTarget := multiClusterAppTargetToAdd(d, multiClusterApp)

		if len(removeTarget.Projects) > 0 {
			log.Printf("[INFO] Removing targets on multi cluster app ID %s", id)
			err = client.MultiClusterApp.ActionRemoveProjects(multiClusterApp, removeTarget)
			if err != nil {
				return diag.FromErr(err)
			}
			if d.HasChange("answers") {
				// answer for removed target has to be deleted manually
				updateApp = true
			}
		}

		if len(addTarget.Projects) > 0 {
			log.Printf("[INFO] Adding targets on multi cluster app ID %s", id)
			err = client.MultiClusterApp.ActionAddProjects(multiClusterApp, addTarget)
			if err != nil {
				return diag.FromErr(err)
			}
		}
	}

	// Update app if needed
	if updateApp {
		log.Printf("[INFO] Updating multi cluster app ID %s", id)

		update := map[string]interface{}{
			"answers":              expandAnswers(d.Get("answers").([]interface{})),
			"members":              expandMembers(d.Get("members").([]interface{})),
			"revisionHistoryLimit": d.Get("revision_history_limit").(int),
			"roles":                toArrayString(d.Get("roles").([]interface{})),
			"templateVersionId":    expandMultiClusterAppTemplateVersionID(d),
			"upgradeStrategy":      expandUpgradeStrategy(d.Get("upgrade_strategy").([]interface{})),
			"annotations":          toMapString(d.Get("annotations").(map[string]interface{})),
			"labels":               toMapString(d.Get("labels").(map[string]interface{})),
		}
		_, err := client.MultiClusterApp.Update(multiClusterApp, update)
		if err != nil {
			return diag.FromErr(err)
		}
	}

	if d.Get("wait").(bool) {
		stateConf := &retry.StateChangeConf{
			Pending:    []string{},
			Target:     []string{"active"},
			Refresh:    multiClusterAppStateRefreshFunc(client, id),
			Timeout:    d.Timeout(schema.TimeoutCreate),
			Delay:      1 * time.Second,
			MinTimeout: 3 * time.Second,
		}
		_, waitErr := stateConf.WaitForStateContext(ctx)
		if waitErr != nil {
			return diag.Errorf("[ERROR] waiting for multi cluster app (%s) to be created: %s", id, waitErr)
		}
	}

	return resourceRancher2MultiClusterAppRead(ctx, d, meta)
}

func resourceRancher2MultiClusterAppDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	id := d.Id()

	log.Printf("[INFO] Deleting multi cluster app ID %s", id)

	client, err := meta.(*Config).ManagementClient()
	if err != nil {
		return diag.FromErr(err)
	}

	multiClusterApp, err := client.MultiClusterApp.ByID(id)
	if err != nil {
		if IsNotFound(err) || IsForbidden(err) {
			log.Printf("[INFO] multi cluster app ID %s not found.", d.Id())
			d.SetId("")
			return nil
		}
		return diag.FromErr(err)
	}

	err = client.MultiClusterApp.Delete(multiClusterApp)
	if err != nil {
		return diag.Errorf("[ERROR] removing multi cluster app: %s", err)
	}

	stateConf := &retry.StateChangeConf{
		Pending:    []string{"removing"},
		Target:     []string{"removed"},
		Refresh:    multiClusterAppStateRefreshFunc(client, id),
		Timeout:    d.Timeout(schema.TimeoutDelete),
		Delay:      1 * time.Second,
		MinTimeout: 3 * time.Second,
	}

	_, waitErr := stateConf.WaitForStateContext(ctx)
	if waitErr != nil {
		return diag.Errorf(
			"[ERROR] waiting for multi cluster app (%s) to be removed: %s", id, waitErr)
	}
	d.SetId("")

	for i := range multiClusterApp.Targets {
		client, err := meta.(*Config).ProjectClient(multiClusterApp.Targets[i].ProjectID)
		if err != nil {
			continue
		}
		mappID := splitProjectIDPart(multiClusterApp.Targets[i].ProjectID) + ":" + multiClusterApp.Targets[i].AppID
		stateConf = &retry.StateChangeConf{
			Pending:    []string{"removing"},
			Target:     []string{"removed"},
			Refresh:    appStateRefreshFunc(client, mappID),
			Timeout:    d.Timeout(schema.TimeoutDelete),
			Delay:      1 * time.Second,
			MinTimeout: 3 * time.Second,
		}
		stateConf.WaitForStateContext(ctx)
	}
	time.Sleep(5 * time.Second)

	return nil
}

func resourceRancher2MultiClusterAppGetVersion(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	catalogName := d.Get("catalog_name").(string)
	appName := d.Get("template_name").(string)
	appVersion := d.Get("template_version").(string)

	if len(appVersion) > 0 {
		return nil
	}

	catalogName = MultiClusterAppTemplatePrefix + catalogName

	appID := catalogName + "-" + appName

	client, err := meta.(*Config).ManagementClient()
	if err != nil {
		return diag.FromErr(err)
	}

	template, err := client.Template.ByID(appID)
	if err != nil {
		return diag.FromErr(err)
	}

	appVersion, err = getLatestVersion(template.VersionLinks)
	if err != nil {
		return diag.FromErr(err)
	}

	d.Set("template_version", appVersion)

	return nil
}

// multiClusterAppStateRefreshFunc returns a retry.StateRefreshFunc, used to watch a Rancher MultiClusterApp.
func multiClusterAppStateRefreshFunc(client *managementClient.Client, appID string) retry.StateRefreshFunc {
	return func() (interface{}, string, error) {
		obj, err := client.MultiClusterApp.ByID(appID)
		if err != nil {
			if IsNotFound(err) || IsForbidden(err) {
				return obj, "removed", nil
			}
			return nil, "", err
		}

		return obj, obj.State, nil
	}
}

func multiClusterAppTargetToRemove(d *schema.ResourceData, mca *managementClient.MultiClusterApp) *managementClient.UpdateMultiClusterAppTargetsInput {
	newTargets := expandTargets(d.Get("targets").([]interface{}))

	removeTarget := &managementClient.UpdateMultiClusterAppTargetsInput{}
	for _, t := range mca.Targets {
		found := false
		for _, newT := range newTargets {
			if t == newT {
				found = true
				break
			}
		}
		if !found {
			var a *managementClient.Answer
			if d.HasChange("answers") {
				for _, answer := range mca.Answers {
					if t.ProjectID == answer.ProjectID {
						a = &answer
						break
					}
				}
			}
			removeTarget.Projects = append(removeTarget.Projects, t.ProjectID)
			if a != nil {
				removeTarget.Answers = append(removeTarget.Answers, *a)
			}
		}
	}

	return removeTarget
}

func multiClusterAppTargetToAdd(d *schema.ResourceData, mca *managementClient.MultiClusterApp) *managementClient.UpdateMultiClusterAppTargetsInput {
	newTargets := expandTargets(d.Get("targets").([]interface{}))

	addTarget := &managementClient.UpdateMultiClusterAppTargetsInput{}
	for _, newT := range newTargets {
		found := false
		for _, t := range mca.Targets {
			if t == newT {
				found = true
				break
			}
		}
		if !found {
			var a *managementClient.Answer
			if d.HasChange("answers") {
				newAnswers := expandAnswers(d.Get("answers").([]interface{}))
				for _, answer := range newAnswers {
					if newT.ProjectID == answer.ProjectID {
						a = &answer
						break
					}
				}
			}
			addTarget.Projects = append(addTarget.Projects, newT.ProjectID)
			if a != nil {
				addTarget.Answers = append(addTarget.Answers, *a)
			}
		}
	}

	return addTarget
}
