package main

import (
	"errors"
	"flag"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
)

type transferProviderFlags struct {
	allow bool
	route config.BBTransferRoute
}

func providerTransferFlags(fs *flag.FlagSet) *transferProviderFlags {
	o := &transferProviderFlags{}
	fs.BoolVar(&o.allow, "allow-provider-change", false, "explicitly change provider; clears account annotation, probe and timing settings")
	fs.StringVar(&o.route.Provider, "provider", "", "expected destination provider (requires --allow-provider-change)")
	fs.StringVar(&o.route.Group, "quota-group", "", "destination quota-group label; does not establish account identity")
	fs.StringVar(&o.route.Model, "model", "", "destination model label; explicitly pass empty to clear")
	fs.StringVar(&o.route.Reasoning, "reasoning", "", "destination reasoning label; explicitly pass empty to clear")
	return o
}

func (o *transferProviderFlags) validate(fs *flag.FlagSet, recover bool) error {
	present := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { present[f.Name] = true })
	any := o.allow || present["allow-provider-change"] || present["provider"] || present["quota-group"] || present["model"] || present["reasoning"]
	if !any {
		return nil
	}
	if recover {
		return errors.New("recover uses the recorded provider plan; destination flags are not accepted")
	}
	if !o.allow || !present["provider"] || !present["quota-group"] || !present["model"] || !present["reasoning"] {
		return errors.New("provider change requires --allow-provider-change, --provider, --quota-group, --model and --reasoning (empty model/reasoning clears)")
	}
	r := o.route
	if !transferLabel.MatchString(r.Provider) || !transferLabel.MatchString(r.Group) || (r.Model != "" && !transferLabel.MatchString(r.Model)) || (r.Reasoning != "" && !transferLabel.MatchString(r.Reasoning)) {
		return errors.New("destination labels are missing or unsupported")
	}
	return nil
}

func (o *transferProviderFlags) plan(provider string) (*config.BBTransferRoute, error) {
	if !o.allow {
		return nil, nil
	}
	if provider == o.route.Provider {
		return nil, errors.New("provider-change opt-in requires a different destination provider")
	}
	return &o.route, nil
}

func transferRecordedRoute(p control.PendingBBTransfer) *config.BBTransferRoute {
	if p.Version != 2 {
		return nil
	}
	return &config.BBTransferRoute{Provider: p.ToProvider, Group: p.Group, Model: p.Model, Reasoning: p.Reasoning}
}
