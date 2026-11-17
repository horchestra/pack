package environment

import "github.com/horchestra/pack/pkg/render"

// Placements converts the environment's keys to the render's placements.
func Placements(keys map[string]Key) map[string]render.Placement {
	out := make(map[string]render.Placement, len(keys))
	for name, key := range keys {
		placement := render.Placement{
			Target: key.Target, Replicas: key.Replicas, Labels: key.Labels, Annotations: key.Annotations, Raw: key.Raw,
		}
		if key.Resources != nil {
			placement.Resources = &render.Resources{Requests: key.Resources.Requests, Limits: key.Resources.Limits}
		}
		for _, item := range key.Env {
			env := render.EnvVar{Name: item.Name, Value: item.Value}
			if item.SecretRef != nil {
				env.Secret = &render.SecretRef{Name: item.SecretRef.Name, Key: item.SecretRef.Key}
			}
			placement.Env = append(placement.Env, env)
		}
		if key.Reach != nil {
			placement.Reach = &render.Reach{Instances: key.Reach.Instances, Ports: key.Reach.Ports}
		}
		out[name] = placement
	}
	return out
}
