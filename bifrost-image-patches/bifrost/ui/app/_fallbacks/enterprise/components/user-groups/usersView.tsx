import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useGetCustomersQuery, useGetDirectoryUsersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { useGetAvailableFilterDataQuery } from "@/lib/store/apis/logsApi";
import { CheckCircle2, Link2, Search, ShieldCheck, UserX, Users, XCircle } from "lucide-react";
import { useMemo, useState } from "react";

type FilterIdentity = {
	id: string;
	name: string;
};

function normalizeIdentity(raw: unknown): FilterIdentity | null {
	if (!raw || typeof raw !== "object") return null;
	const record = raw as Record<string, unknown>;
	const id = String(record.id ?? record.ID ?? "").trim();
	const name = String(record.name ?? record.Name ?? id).trim();
	if (!id) return null;
	return { id, name: name || id };
}

function StatTile({ label, value }: { label: string; value: number | string }) {
	return (
		<div className="border-border bg-card rounded-sm border px-4 py-3">
			<div className="text-muted-foreground text-xs">{label}</div>
			<div className="text-foreground mt-1 text-xl font-semibold">{value}</div>
		</div>
	);
}

export default function UsersView() {
	const [search, setSearch] = useState("");
	const { data: filterData, isLoading: filterDataLoading } = useGetAvailableFilterDataQuery();
	const {
		data: directoryUsersData,
		isLoading: directoryUsersLoading,
		isError: directoryUsersError,
	} = useGetDirectoryUsersQuery({ limit: 250, offset: 0, search: search.trim() || undefined });
	const { data: virtualKeysData, isLoading: virtualKeysLoading } = useGetVirtualKeysQuery({ limit: 100, offset: 0 });
	const { data: teamsData, isLoading: teamsLoading } = useGetTeamsQuery({ limit: 100, offset: 0 });
	const { data: customersData, isLoading: customersLoading } = useGetCustomersQuery({ limit: 100, offset: 0 });

	const loggedUsers = useMemo(() => {
		const rawUsers = filterData?.users ?? [];
		const normalized = rawUsers.map(normalizeIdentity).filter((user): user is FilterIdentity => Boolean(user));
		const deduped = new Map<string, FilterIdentity>();
		for (const user of normalized) {
			deduped.set(user.id, user);
		}
		return [...deduped.values()].sort((a, b) => a.name.localeCompare(b.name));
	}, [filterData?.users]);

	const directoryUsers = useMemo(
		() =>
			(directoryUsersData?.users ?? [])
				.map((user) => ({
					id: user.id || user.username || user.email || "",
					name: user.name || user.username || user.email || user.id,
					username: user.username || "",
					email: user.email || "",
					enabled: user.enabled,
					emailVerified: user.email_verified,
					source: "keycloak" as const,
				}))
				.filter((user) => user.id),
		[directoryUsersData?.users],
	);

	const users =
		directoryUsers.length > 0 || !directoryUsersError
			? directoryUsers
			: loggedUsers.map((user) => ({
					...user,
					username: user.name,
					email: "",
					enabled: true,
					emailVerified: false,
					source: "logs" as const,
				}));

	const filteredUsers = useMemo(() => {
		const query = search.trim().toLowerCase();
		if (!query) return users;
		return users.filter(
			(user) =>
				user.id.toLowerCase().includes(query) ||
				user.name.toLowerCase().includes(query) ||
				user.username.toLowerCase().includes(query) ||
				user.email.toLowerCase().includes(query),
		);
	}, [search, users]);

	const activeVirtualKeys = useMemo(() => (virtualKeysData?.virtual_keys ?? []).filter((key) => key.is_active), [virtualKeysData?.virtual_keys]);
	const governedVirtualKeys = useMemo(
		() => activeVirtualKeys.filter((key) => key.team_id || key.customer_id || key.budgets?.length || key.rate_limit_id || key.rate_limit),
		[activeVirtualKeys],
	);

	const isLoading = directoryUsersLoading || filterDataLoading || virtualKeysLoading || teamsLoading || customersLoading;
	const sourceLabel = directoryUsersError ? "Log-discovered users" : "Keycloak registered users";

	return (
		<div className="mx-auto flex w-full max-w-7xl flex-col gap-5 py-4">
			<div className="flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
				<div>
					<div className="flex items-center gap-2">
						<Users className="text-primary h-5 w-5" />
						<h1 className="text-foreground text-xl font-semibold">Users</h1>
						<Badge variant="success">Unlocked</Badge>
					</div>
					<p className="text-muted-foreground mt-1 text-sm">
						Registered Keycloak users are shown here for admin review and can be governed through virtual keys, teams, customers, budgets, and rate limits.
					</p>
				</div>
				<Button asChild variant="outline" size="sm">
					<a href="/workspace/governance/virtual-keys">
						<ShieldCheck className="h-4 w-4" />
						Virtual keys
					</a>
				</Button>
			</div>

			<div className="grid gap-3 md:grid-cols-4">
				<StatTile label="Registered users" value={directoryUsersData?.total_count ?? users.length} />
				<StatTile label="Active virtual keys" value={activeVirtualKeys.length} />
				<StatTile label="Governed virtual keys" value={governedVirtualKeys.length} />
				<StatTile label="Teams" value={teamsData?.total_count ?? teamsData?.teams?.length ?? 0} />
			</div>

			<div className="border-border bg-card rounded-sm border">
				<div className="border-border flex flex-col gap-3 border-b p-4 md:flex-row md:items-center md:justify-between">
					<div>
						<h2 className="text-foreground text-sm font-medium">{sourceLabel}</h2>
						<p className="text-muted-foreground mt-1 text-xs">
							{directoryUsersError
								? "Keycloak directory lookup is unavailable, so Bifrost is showing users discovered from request logs."
								: "These users come from the configured Keycloak realm."}
						</p>
					</div>
					<div className="relative w-full md:w-72">
						<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
						<Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search users" className="pl-9" />
					</div>
				</div>

				{isLoading ? (
					<div className="space-y-3 p-4">
						<Skeleton className="h-9 w-full" />
						<Skeleton className="h-9 w-full" />
						<Skeleton className="h-9 w-full" />
					</div>
				) : filteredUsers.length > 0 ? (
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead>User</TableHead>
								<TableHead>Email</TableHead>
								<TableHead>Status</TableHead>
								<TableHead>User ID</TableHead>
								<TableHead>Governance path</TableHead>
								<TableHead className="text-right">Logs</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{filteredUsers.map((user) => (
								<TableRow key={user.id}>
									<TableCell>
										<div className="font-medium">{user.name}</div>
										<div className="text-muted-foreground text-xs">{user.username}</div>
									</TableCell>
									<TableCell>{user.email || <span className="text-muted-foreground text-xs">No email</span>}</TableCell>
									<TableCell>
										<div className="flex flex-wrap gap-1.5">
											<Badge variant={user.enabled ? "success" : "secondary"}>
												{user.enabled ? <CheckCircle2 className="h-3 w-3" /> : <XCircle className="h-3 w-3" />}
												{user.enabled ? "Enabled" : "Disabled"}
											</Badge>
											{user.email && (
												<Badge variant={user.emailVerified ? "outline" : "secondary"}>
													{user.emailVerified ? "Verified" : "Unverified"}
												</Badge>
											)}
										</div>
									</TableCell>
									<TableCell>
										<code className="bg-muted rounded-sm px-1.5 py-0.5 text-xs">{user.id}</code>
									</TableCell>
									<TableCell>
										<div className="flex flex-wrap gap-1.5">
											<Badge variant="outline">user_id</Badge>
											<Badge variant="outline">virtual key</Badge>
											<Badge variant="outline">team</Badge>
											<Badge variant="outline">customer</Badge>
										</div>
									</TableCell>
									<TableCell className="text-right">
										<Button asChild variant="ghost" size="sm">
											<a href={`/workspace/logs?user_ids=${encodeURIComponent(user.id)}`}>
												<Link2 className="h-4 w-4" />
												View
											</a>
										</Button>
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>
				) : (
					<div className="flex min-h-60 flex-col items-center justify-center gap-2 p-8 text-center">
						{directoryUsersError ? (
							<UserX className="text-muted-foreground h-10 w-10" strokeWidth={1.5} />
						) : (
							<Users className="text-muted-foreground h-10 w-10" strokeWidth={1.5} />
						)}
						<div className="text-foreground text-sm font-medium">{search ? "No matching users" : "No registered users"}</div>
						<p className="text-muted-foreground max-w-md text-sm">
							{directoryUsersError
								? "Check the Bifrost Keycloak admin environment variables and Keycloak availability."
								: "Users created in the configured Keycloak realm will appear here."}
						</p>
					</div>
				)}
			</div>

			<div className="grid gap-3 md:grid-cols-3">
				<StatTile label="Customers" value={customersData?.total_count ?? customersData?.customers?.length ?? 0} />
				<StatTile label="Log teams" value={filterData?.teams?.length ?? 0} />
				<StatTile label="Log business units" value={filterData?.business_units?.length ?? 0} />
			</div>
		</div>
	);
}
