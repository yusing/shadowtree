package systemsandbox

import "github.com/yusing/shadowtree/internal/recipe"

// ImageRequest preserves every recipe contribution used to plan one system
// image. Root remains the recipe whose lifecycle is executed.
type ImageRequest struct {
	Root          recipe.Resolved
	Contributions []ImageContribution
}

// ImageContribution is one resolved recipe's system-image input and origin.
type ImageContribution struct {
	Resolved       recipe.Resolved
	ConfigIdentity string
	Workdir        string
	ReferenceRoute []ReferenceRouteStep
}

// ReferenceRouteStep identifies one recipe-reference edge leading to a
// contribution.
type ReferenceRouteStep struct {
	ConfigIdentity string
	Recipe         string
	Stage          string
	Reference      string
}

// PlanComposition plans the preserved contribution graph. The current image
// renderer still consumes the merged root contract; provider composition is
// implemented at this boundary by the next product slice.
func PlanComposition(request ImageRequest, sourceDir string) (ImagePlan, error) {
	return PlanImages(request.Root, sourceDir)
}
