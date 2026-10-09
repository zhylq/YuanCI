package pipeline

// DeploymentRepositoryLabel identifies an administrator-approved repository grant.
func DeploymentRepositoryLabel(provider, externalID string) string {
	return "yuanci.deploy." + provider + "." + externalID
}

// ValidatePlanDeployment checks the persisted marker as well as top-level metadata.
// Creation APIs also accept compiled plans directly, so YAML validation is insufficient.
func ValidatePlanDeployment(plan Plan) error {
	environment := ""
	if plan.Deployment != nil {
		environment = plan.Deployment.Environment
		if !namePattern.MatchString(environment) {
			return ValidationError{"deployment.environment", "is invalid"}
		}
		if len(plan.Stages) == 0 {
			return ValidationError{"deployment", "requires executable jobs"}
		}
	}
	for _, stage := range plan.Stages {
		if environment != "" && len(stage.Jobs) == 0 {
			return ValidationError{"deployment", "requires executable jobs"}
		}
		for _, job := range stage.Jobs {
			if job.Deployment != environment {
				return ValidationError{"deployment", "job marker does not match plan metadata"}
			}
			if environment != "" && job.Retry != 0 {
				return ValidationError{"retry", "deployments cannot retry"}
			}
		}
	}
	return nil
}
