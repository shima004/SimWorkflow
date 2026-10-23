package experiment

// Roles group each platoon type with its corresponding centre type.
func Roles() []string {
	return []string{"fireBrigade", "policeForce", "ambulanceTeam"}
}

func ValidRole(role string) bool {
	for _, candidate := range Roles() {
		if role == candidate {
			return true
		}
	}
	return false
}

func (c Configuration) ResourcesForRole(role string) Resources {
	if resources, ok := c.ResourcesByRole[role]; ok {
		return resources
	}
	return c.Resources
}
