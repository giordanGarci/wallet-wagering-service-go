package httpapi

import "go.uber.org/fx"

// Module is the "api" role: authentication and the business endpoints.
// Each handler joins the server through AsRoute and is wrapped with an
// Authenticator policy (Internal, Provider or Require), for example:
//
//	fx.Provide(httpapi.AsRoute(func(h *WalletHandlers, auth *Authenticator) httpapi.Route {
//		return httpapi.Route{Pattern: "POST /wallets", Handler: auth.Internal(h)}
//	}))
var Module = fx.Module("httpapi",
	fx.Provide(
		NewAuthenticator,
		NewWalletHandlers,
		AsRoute(OpenWalletRoute),
		AsRoute(GetWalletRoute),
	),
)
