import { DriveStep } from 'driver.js'

// steps.ts defines translated administrator, personal, and team onboarding
// sequences for useOnboardingTour. Interactive steps identify controls through
// stable selectors, while team steps also describe the route that must be active
// before the target can be highlighted.

// RoutedDriveStep extends a driver step with navigation required before locating
// its target.
export interface RoutedDriveStep extends DriveStep {
  // route is resolved and awaited before target lookup when the tour crosses pages.
  route?: {
    path: string // Destination application route path.
    query?: Record<string, string> // Destination route query values.
  }
}

// getAdminSteps builds the administrator sequence for creating groups,
// providers, and API keys. The translation function supplies display text, and
// close-only popovers mark controls that require interaction before advancing.
export const getAdminSteps = (t: (key: string) => string): DriveStep[] => {
  const allSteps: DriveStep[] = [
    // Welcome and tour navigation.
    {
      popover: {
        title: t('onboarding.admin.welcome.title'),
        description: t('onboarding.admin.welcome.description'),
        align: 'center',
        nextBtnText: t('onboarding.admin.welcome.nextBtn'),
        prevBtnText: t('onboarding.admin.welcome.prevBtn'),
      },
    },

    // Group creation controls in their interaction order.
    {
      element: '#sidebar-group-manage',
      popover: {
        title: t('onboarding.admin.groupManage.title'),
        description: t('onboarding.admin.groupManage.description'),
        side: 'right',
        align: 'center',
        showButtons: ['close'],
      },
    },
    {
      element: '[data-tour="groups-create-btn"]',
      popover: {
        title: t('onboarding.admin.createGroup.title'),
        description: t('onboarding.admin.createGroup.description'),
        side: 'bottom',
        align: 'end',
        showButtons: ['close'],
      },
    },
    {
      element: '[data-tour="group-form-name"]',
      popover: {
        title: t('onboarding.admin.groupName.title'),
        description: t('onboarding.admin.groupName.description'),
        side: 'right',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="group-form-multiplier"]',
      popover: {
        title: t('onboarding.admin.groupMultiplier.title'),
        description: t('onboarding.admin.groupMultiplier.description'),
        side: 'right',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="group-form-exclusive"]',
      popover: {
        title: t('onboarding.admin.groupExclusive.title'),
        description: t('onboarding.admin.groupExclusive.description'),
        side: 'top',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="group-form-submit"]',
      popover: {
        title: t('onboarding.admin.groupSubmit.title'),
        description: t('onboarding.admin.groupSubmit.description'),
        side: 'left',
        align: 'center',
        showButtons: ['close'],
      },
    },

    // Provider authorization controls in their interaction order.
    {
      element: '#sidebar-channel-manage',
      popover: {
        title: t('onboarding.admin.providerManage.title'),
        description: t('onboarding.admin.providerManage.description'),
        side: 'right',
        align: 'center',
        showButtons: ['close'],
      },
    },
    {
      element: '[data-tour="providers-create-btn"]',
      popover: {
        title: t('onboarding.admin.createProvider.title'),
        description: t('onboarding.admin.createProvider.description'),
        side: 'bottom',
        align: 'end',
        showButtons: ['close'],
      },
    },
    {
      element: '[data-tour="provider-form-name"]',
      popover: {
        title: t('onboarding.admin.providerName.title'),
        description: t('onboarding.admin.providerName.description'),
        side: 'right',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="provider-form-platform"]',
      popover: {
        title: t('onboarding.admin.providerPlatform.title'),
        description: t('onboarding.admin.providerPlatform.description'),
        side: 'right',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="provider-form-type"]',
      popover: {
        title: t('onboarding.admin.providerType.title'),
        description: t('onboarding.admin.providerType.description'),
        side: 'right',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="provider-form-priority"]',
      popover: {
        title: t('onboarding.admin.providerPriority.title'),
        description: t('onboarding.admin.providerPriority.description'),
        side: 'top',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="provider-form-groups"]',
      popover: {
        title: t('onboarding.admin.providerGroups.title'),
        description: t('onboarding.admin.providerGroups.description'),
        side: 'top',
        align: 'center',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="provider-form-submit"]',
      popover: {
        title: t('onboarding.admin.providerSubmit.title'),
        description: t('onboarding.admin.providerSubmit.description'),
        side: 'left',
        align: 'center',
        showButtons: ['close'],
      },
    },

    // API-key creation controls in their interaction order.
    {
      element: '[data-tour="sidebar-my-keys"]',
      popover: {
        title: t('onboarding.admin.keyManage.title'),
        description: t('onboarding.admin.keyManage.description'),
        side: 'right',
        align: 'center',
        showButtons: ['close'],
      },
    },
    {
      element: '[data-tour="keys-create-btn"]',
      popover: {
        title: t('onboarding.admin.createKey.title'),
        description: t('onboarding.admin.createKey.description'),
        side: 'bottom',
        align: 'end',
        showButtons: ['close'],
      },
    },
    {
      element: '[data-tour="key-form-name"]',
      popover: {
        title: t('onboarding.admin.keyName.title'),
        description: t('onboarding.admin.keyName.description'),
        side: 'right',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="key-form-group"]',
      popover: {
        title: t('onboarding.admin.keyGroup.title'),
        description: t('onboarding.admin.keyGroup.description'),
        side: 'right',
        align: 'start',
        showButtons: ['next', 'previous'],
      },
    },
    {
      element: '[data-tour="key-form-submit"]',
      popover: {
        title: t('onboarding.admin.keySubmit.title'),
        description: t('onboarding.admin.keySubmit.description'),
        side: 'left',
        align: 'center',
        showButtons: ['close'],
      },
    },
  ]

  return allSteps
}

// getUserSteps builds the personal API-key onboarding sequence using translated
// text. Close-only popovers identify interactive navigation and submit controls,
// while form fields provide the driver navigation buttons.
export const getUserSteps = (t: (key: string) => string): DriveStep[] => [
  {
    popover: {
      title: t('onboarding.user.welcome.title'),
      description: t('onboarding.user.welcome.description'),
      align: 'center',
      nextBtnText: t('onboarding.user.welcome.nextBtn'),
      prevBtnText: t('onboarding.user.welcome.prevBtn'),
    },
  },
  {
    element: '[data-tour="sidebar-my-keys"]',
    popover: {
      title: t('onboarding.user.keyManage.title'),
      description: t('onboarding.user.keyManage.description'),
      side: 'right',
      align: 'center',
      showButtons: ['close'],
    },
  },
  {
    element: '[data-tour="keys-create-btn"]',
    popover: {
      title: t('onboarding.user.createKey.title'),
      description: t('onboarding.user.createKey.description'),
      side: 'bottom',
      align: 'end',
      showButtons: ['close'],
    },
  },
  {
    element: '[data-tour="key-form-name"]',
    popover: {
      title: t('onboarding.user.keyName.title'),
      description: t('onboarding.user.keyName.description'),
      side: 'right',
      align: 'start',
      showButtons: ['next', 'previous'],
    },
  },
  {
    element: '[data-tour="key-form-group"]',
    popover: {
      title: t('onboarding.user.keyGroup.title'),
      description: t('onboarding.user.keyGroup.description'),
      side: 'right',
      align: 'start',
      showButtons: ['next', 'previous'],
    },
  },
  {
    element: '[data-tour="key-form-submit"]',
    popover: {
      title: t('onboarding.user.keySubmit.title'),
      description: t('onboarding.user.keySubmit.description'),
      side: 'left',
      align: 'center',
      showButtons: ['close'],
    },
  },
]

// getTeamSteps builds team onboarding for ownership and membership state
// supplied by the caller. Users without a team see creation guidance; existing
// teams show owner or member controls and route to team keys and usage.
export const getTeamSteps = (
  t: (key: string) => string,
  isOwner: boolean,
  hasTeam = true,
): RoutedDriveStep[] => {
  if (!hasTeam) {
    return [
      {
        route: { path: '/team' },
        popover: {
          title: t('onboarding.team.welcome.title'),
          description: t('onboarding.team.welcome.noTeamDescription'),
          align: 'center',
          nextBtnText: t('onboarding.team.welcome.nextBtn'),
        },
      },
      {
        route: { path: '/team' },
        element: '[data-tour="team-create-form"]',
        popover: {
          title: t('onboarding.team.createTeam.title'),
          description: t('onboarding.team.createTeam.description'),
          side: 'top',
          align: 'center',
        },
      },
      {
        route: { path: '/team' },
        element: '[data-tour="sidebar-my-keys"]',
        popover: {
          title: t('onboarding.team.keyPage.title'),
          description: t('onboarding.team.keyPage.noTeamDescription'),
          side: 'right',
          align: 'center',
        },
      },
      {
        route: { path: '/team' },
        element: '[data-tour="sidebar-usage"]',
        popover: {
          title: t('onboarding.team.usagePage.title'),
          description: t('onboarding.team.usagePage.noTeamDescription'),
          side: 'right',
          align: 'center',
          doneBtnText: t('onboarding.team.done'),
        },
      },
    ]
  }

  const overviewSteps: RoutedDriveStep[] = isOwner
    ? [
        {
          route: { path: '/team' },
          element: '[data-tour="team-members"]',
          popover: {
            title: t('onboarding.team.members.title'),
            description: t('onboarding.team.members.description'),
            side: 'top',
            align: 'center',
          },
        },
        {
          route: { path: '/team' },
          element: '[data-tour="team-invitations"]',
          popover: {
            title: t('onboarding.team.invitations.title'),
            description: t('onboarding.team.invitations.description'),
            side: 'top',
            align: 'center',
          },
        },
        {
          route: { path: '/team' },
          element: '[data-tour="team-settings-tab"]',
          popover: {
            title: t('onboarding.team.settings.title'),
            description: t('onboarding.team.settings.description'),
            side: 'bottom',
            align: 'center',
          },
        },
      ]
    : [
        {
          route: { path: '/team' },
          element: '[data-tour="team-limit-progress"]',
          popover: {
            title: t('onboarding.team.limits.title'),
            description: t('onboarding.team.limits.description'),
            side: 'top',
            align: 'center',
          },
        },
      ]

  return [
    {
      route: { path: '/team' },
      popover: {
        title: t('onboarding.team.welcome.title'),
        description: t(
          isOwner
            ? 'onboarding.team.welcome.ownerDescription'
            : 'onboarding.team.welcome.memberDescription',
        ),
        align: 'center',
        nextBtnText: t('onboarding.team.welcome.nextBtn'),
      },
    },
    ...overviewSteps,
    {
      route: { path: '/team' },
      element: '[data-tour="sidebar-my-keys"]',
      popover: {
        title: t('onboarding.team.keyPage.title'),
        description: t('onboarding.team.keyPage.description'),
        side: 'right',
        align: 'center',
      },
    },
    {
      route: { path: '/keys', query: { scope: 'team' } },
      element: '[data-tour="keys-scope-switch"]',
      popover: {
        title: t('onboarding.team.keyScope.title'),
        description: t('onboarding.team.keyScope.description'),
        side: 'bottom',
        align: 'end',
      },
    },
    {
      route: { path: '/keys', query: { scope: 'team' } },
      element: '[data-tour="keys-create-btn"]',
      popover: {
        title: t('onboarding.team.createKey.title'),
        description: t('onboarding.team.createKey.description'),
        side: 'bottom',
        align: 'end',
      },
    },
    {
      route: { path: '/keys', query: { scope: 'team' } },
      element: '[data-tour="sidebar-usage"]',
      popover: {
        title: t('onboarding.team.usagePage.title'),
        description: t('onboarding.team.usagePage.description'),
        side: 'right',
        align: 'center',
      },
    },
    {
      route: { path: '/usage' },
      element: isOwner
        ? '[data-tour="team-member-usage-charts"]'
        : '[data-tour="team-usage-records"]',
      popover: {
        title: t(
          isOwner ? 'onboarding.team.ownerUsage.title' : 'onboarding.team.memberUsage.title',
        ),
        description: t(
          isOwner
            ? 'onboarding.team.ownerUsage.description'
            : 'onboarding.team.memberUsage.description',
        ),
        side: 'top',
        align: 'center',
        doneBtnText: t('onboarding.team.done'),
      },
    },
  ]
}
