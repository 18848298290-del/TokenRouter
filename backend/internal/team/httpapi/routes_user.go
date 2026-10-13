package httpapi

import "github.com/gin-gonic/gin"

// Current-user team routes inherit the caller's authentication, rate limiting
// and auditing middleware. Selected ownership/status/teardown operations
// additionally use the supplied step-up handler, while endpoint methods pass
// the authenticated account to the service for team permissions.

// RegisterUserRoutes binds current-user team endpoints beneath the
// authenticated group. Assembly installs authentication, rate limits and
// auditing first; supplied stepUp guards status, dissolution and
// ownership-transfer start/resolve/cancel, while the service authorizes team
// roles for each action.
func RegisterUserRoutes(authenticated *gin.RouterGroup, endpoint *UserHandler, stepUp gin.HandlerFunc) {
	team := authenticated.Group("/team")
	{
		team.GET("", endpoint.GetCurrent)
		team.POST("", endpoint.Create)
		team.PATCH("", endpoint.Update)
		team.PATCH("/default-member-limits", endpoint.UpdateDefaultMemberLimits)
		team.POST("/status", stepUp, endpoint.SetStatus)
		team.DELETE("", stepUp, endpoint.Dissolve)
		team.GET("/members", endpoint.ListMembers)
		team.GET("/usage", endpoint.GetUsageSummary)
		team.GET("/usage/members", endpoint.ListMemberUsageSeries)
		team.GET("/usage/logs", endpoint.ListUsageLogs)
		team.GET("/keys", endpoint.ListTeamKeys)
		team.POST("/keys/:id/disable", endpoint.DisableTeamKey)
		team.POST("/keys/:id/enable", endpoint.EnableTeamKey)
		team.DELETE("/keys/:id", endpoint.DeleteTeamKey)
		team.DELETE("/members/:user_id", endpoint.RemoveMember)
		team.PATCH("/members/:user_id/limits", endpoint.UpdateMemberLimits)
		team.POST("/members/:user_id/usage/reset", endpoint.ResetMemberUsage)
		team.POST("/leave", endpoint.Leave)
		team.GET("/invitations", endpoint.ListInvitations)
		team.POST("/invitations", endpoint.Invite)
		team.POST("/invitations/preview", endpoint.PreviewInvitation)
		team.POST("/invitations/resolve", endpoint.ResolveInvitation)
		team.POST("/invitations/:id/reissue", endpoint.ReissueInvitation)
		team.DELETE("/invitations/:id", endpoint.RevokeInvitation)
		team.POST("/ownership-transfer", stepUp, endpoint.StartOwnershipTransfer)
		team.POST("/ownership-transfer/resolve", stepUp, endpoint.ResolveOwnershipTransfer)
		team.DELETE("/ownership-transfer", stepUp, endpoint.CancelOwnershipTransfer)
	}
}
