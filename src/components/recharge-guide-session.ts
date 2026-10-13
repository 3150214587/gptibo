// Fictional JSON for the inert walkthrough; never an authenticated session.
export const guideSession = JSON.stringify({
  user: { id: 'user-000000000000000000000000', name: 'Alex', email: 'alex@example.com', image: 'https://cdn.auth0.com/avatars/al.png', picture: 'https://cdn.auth0.com/avatars/al.png', idp: 'auth0', iat: 1790726400, mfa: false },
  expires: '2026-10-30T01:00:00.000Z',
  account: { id: '00000000-0000-0000-0000-000000000000', planType: 'free', structure: 'personal', isDelinquent: false, isFedrampCompliant: false },
  accessToken: 'DEMO_ONLY_NOT_A_VALID_SESSION',
  authProvider: 'openai',
}, null, 2)
