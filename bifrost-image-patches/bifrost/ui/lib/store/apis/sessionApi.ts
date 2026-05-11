import { baseApi, clearAuthStorage } from "./baseApi";

export interface LoginRequest {
	username: string;
	password: string;
}

export interface LoginResponse {
	message: string;
}

export interface IsAuthEnabledResponse {
	is_auth_enabled: boolean;
	has_valid_token: boolean;
	sso_enabled?: boolean;
	sso_login_url?: string;
}

export interface LogoutResponse {
	message: string;
}

export interface SessionUser {
	id?: string;
	name?: string;
	email?: string;
	avatar_url?: string;
	provider?: string;
}

export interface MeResponse {
	user: SessionUser;
}

export interface DirectoryUser {
	id: string;
	username: string;
	name: string;
	email?: string;
	first_name?: string;
	last_name?: string;
	enabled: boolean;
	email_verified: boolean;
	created_at?: string;
}

export interface DirectoryUsersResponse {
	users: DirectoryUser[];
	count: number;
	total_count: number;
	source: string;
}

export interface DirectoryUsersParams {
	limit?: number;
	offset?: number;
	search?: string;
}

export const sessionApi = baseApi.injectEndpoints({
	overrideExisting: false,
	endpoints: (builder) => ({
		// Check if auth is enabled
		isAuthEnabled: builder.query<IsAuthEnabledResponse, void>({
			query: () => ({
				url: "/session/is-auth-enabled",
				method: "GET",
			}),
		}),
		// Login endpoint
		login: builder.mutation<LoginResponse, LoginRequest>({
			query: (credentials) => ({
				url: "/session/login",
				method: "POST",
				body: credentials,
			}),
			invalidatesTags: ["Sessions"],
		}),

		// Logout endpoint
		logout: builder.mutation<LogoutResponse, void>({
			query: () => ({
				url: "/session/logout",
				method: "POST",
			}),
			// After logout, clear token and all cached data
			async onQueryStarted(arg, { queryFulfilled }) {
				try {
					await queryFulfilled;
				} catch {
				} finally {
					clearAuthStorage();
				}
			},
			invalidatesTags: ["Config", "Providers", "Logs", "VirtualKeys", "Teams", "Customers", "Budgets", "RateLimits", "Sessions"],
		}),

		me: builder.query<MeResponse, void>({
			query: () => ({
				url: "/session/me",
				method: "GET",
			}),
			providesTags: ["Sessions"],
		}),

		getDirectoryUsers: builder.query<DirectoryUsersResponse, DirectoryUsersParams | void>({
			query: (params) => ({
				url: "/session/directory-users",
				method: "GET",
				params: {
					...(params?.limit && { limit: params.limit }),
					...(params?.offset !== undefined && { offset: params.offset }),
					...(params?.search && { search: params.search }),
				},
			}),
			providesTags: ["Users"],
		}),
	}),
});

export const { useIsAuthEnabledQuery, useLoginMutation, useLogoutMutation, useMeQuery, useGetDirectoryUsersQuery } = sessionApi;
