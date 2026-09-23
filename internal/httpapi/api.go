package httpapi

import "go.uber.org/fx"

// Module is the "api" role: the business endpoints. Each handler joins the
// server through AsRoute, for example:
//
//	fx.Provide(httpapi.AsRoute(func(h *WalletHandler) httpapi.Route {
//		return httpapi.Route{Pattern: "POST /wallets", Handler: h}
//	}))
//
// Business endpoints arrive in later tickets; the module is already selected
// by APP_ROLES so they only need to be added here.
var Module = fx.Module("httpapi")
