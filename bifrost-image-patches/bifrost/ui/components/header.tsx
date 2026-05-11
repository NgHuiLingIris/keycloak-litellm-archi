import { useLogoutMutation, useMeQuery, useGetCoreConfigQuery } from "@/lib/store";
import { useNavigate } from "@tanstack/react-router";
import { LogOut } from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "./ui/avatar";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";
import { ThemeToggle } from "./themeToggle";
import { Separator } from "./ui/separator";

const getInitials = (value?: string) => {
	if (!value) return "U";
	const parts = value
		.replace(/@.*/, "")
		.split(/\s+/)
		.filter(Boolean);
	if (parts.length === 0) return "U";
	return parts
		.slice(0, 2)
		.map((part) => part[0]?.toUpperCase())
		.join("");
};

export default function Header({ title }: { title: string }) {
	const navigate = useNavigate();
	const { data: coreConfig } = useGetCoreConfigQuery({});
	const isAuthEnabled = coreConfig?.auth_config?.is_enabled || false;
	const { data: sessionProfileData } = useMeQuery(undefined, {
		skip: !isAuthEnabled,
	});
	const [logout] = useLogoutMutation();
	const user = sessionProfileData?.user;
	const displayName = user?.name || user?.email || "User";

	const handleLogout = async () => {
		try {
			await logout().unwrap();
		} finally {
			navigate({ to: "/login" });
		}
	};

	return (
		<div className="bg-background fixed top-0 right-0 left-(--sidebar-width) z-10">
			<div className="flex items-center justify-between px-3">
				<div className="p-3 font-semibold">{title}</div>
				<div className="flex items-center gap-3">
					<ThemeToggle />
					{isAuthEnabled && user ? (
						<Popover>
							<PopoverTrigger asChild>
								<button type="button" className="rounded-full" aria-label="User menu">
									<Avatar className="h-8 w-8 border">
										{user.avatar_url && <AvatarImage src={user.avatar_url} alt={displayName} />}
										<AvatarFallback className="text-xs font-medium">{getInitials(displayName)}</AvatarFallback>
									</Avatar>
								</button>
							</PopoverTrigger>
							<PopoverContent align="end" className="w-64 p-0">
								<div className="flex flex-col">
									<div className="flex items-center gap-3 px-4 py-3">
										<Avatar className="h-9 w-9 border">
											{user.avatar_url && <AvatarImage src={user.avatar_url} alt={displayName} />}
											<AvatarFallback className="text-xs font-medium">{getInitials(displayName)}</AvatarFallback>
										</Avatar>
										<div className="min-w-0">
											<p className="truncate text-sm font-medium">{displayName}</p>
											{user.email && <p className="text-muted-foreground truncate text-xs">{user.email}</p>}
										</div>
									</div>
									<Separator />
									<button
										onClick={handleLogout}
										className="hover:bg-accent hover:text-accent-foreground flex w-full items-center gap-2 px-4 py-2.5 text-left text-sm transition-colors"
										type="button"
									>
										<LogOut className="h-4 w-4" strokeWidth={2} />
										<span>Logout</span>
									</button>
								</div>
							</PopoverContent>
						</Popover>
					) : null}
				</div>
			</div>
			<Separator className="w-full" />
		</div>
	);
}
