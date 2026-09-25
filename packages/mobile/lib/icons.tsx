import type { ComponentProps } from "react";
// One import per glyph, from its own module. The package root re-exports every
// icon it ships, and a barrel is not tree-shaken here by default: a production
// export that imported the root carried 4,330 modules against 2,480 on main for
// the same screens. These are the only icons the app draws.
import Activity from "lucide-react-native/icons/activity";
import CircleAlert from "lucide-react-native/icons/circle-alert";
import TriangleAlert from "lucide-react-native/icons/triangle-alert";
import Archive from "lucide-react-native/icons/archive";
import ArrowDown from "lucide-react-native/icons/arrow-down";
import ArrowUp from "lucide-react-native/icons/arrow-up";
import Bell from "lucide-react-native/icons/bell";
import Bookmark from "lucide-react-native/icons/bookmark";
import CameraOff from "lucide-react-native/icons/camera-off";
import Check from "lucide-react-native/icons/check";
import CircleCheck from "lucide-react-native/icons/circle-check";
import CircleDashedCheck from "lucide-react-native/icons/circle-dashed-check";
import SquareCheck from "lucide-react-native/icons/square-check";
import ChevronDown from "lucide-react-native/icons/chevron-down";
import ChevronLeft from "lucide-react-native/icons/chevron-left";
import ChevronRight from "lucide-react-native/icons/chevron-right";
import ChevronUp from "lucide-react-native/icons/chevron-up";
import Circle from "lucide-react-native/icons/circle";
import Clock from "lucide-react-native/icons/clock";
import Cloud from "lucide-react-native/icons/cloud";
import Code from "lucide-react-native/icons/code";
import Command from "lucide-react-native/icons/command";
import Copy from "lucide-react-native/icons/copy";
import CornerDownLeft from "lucide-react-native/icons/corner-down-left";
import CornerDownRight from "lucide-react-native/icons/corner-down-right";
import CornerUpRight from "lucide-react-native/icons/corner-up-right";
import Cpu from "lucide-react-native/icons/cpu";
import Delete from "lucide-react-native/icons/delete";
import Disc from "lucide-react-native/icons/disc";
import CloudDownload from "lucide-react-native/icons/cloud-download";
import Pen from "lucide-react-native/icons/pen";
import PenLine from "lucide-react-native/icons/pen-line";
import ExternalLink from "lucide-react-native/icons/external-link";
import FeatherGlyph from "lucide-react-native/icons/feather";
import File from "lucide-react-native/icons/file";
import FileText from "lucide-react-native/icons/file-text";
import Folder from "lucide-react-native/icons/folder";
import FolderOpen from "lucide-react-native/icons/folder-open";
import Flag from "lucide-react-native/icons/flag";
import GitBranch from "lucide-react-native/icons/git-branch";
import GitMerge from "lucide-react-native/icons/git-merge";
import GitPullRequest from "lucide-react-native/icons/git-pull-request";
import Globe from "lucide-react-native/icons/globe";
import Grid2x2 from "lucide-react-native/icons/grid-2x2";
import CircleQuestionMark from "lucide-react-native/icons/circle-question-mark";
import Image from "lucide-react-native/icons/image";
import Inbox from "lucide-react-native/icons/inbox";
import Info from "lucide-react-native/icons/info";
import Italic from "lucide-react-native/icons/italic";
import KeyRound from "lucide-react-native/icons/key-round";
import Layers from "lucide-react-native/icons/layers";
import Link from "lucide-react-native/icons/link";
import List from "lucide-react-native/icons/list";
import LoaderCircle from "lucide-react-native/icons/loader-circle";
import LogOut from "lucide-react-native/icons/log-out";
import Map from "lucide-react-native/icons/map";
import Maximize from "lucide-react-native/icons/maximize";
import Menu from "lucide-react-native/icons/menu";
import MessageCircle from "lucide-react-native/icons/message-circle";
import MessageSquare from "lucide-react-native/icons/message-square";
import Mic from "lucide-react-native/icons/mic";
import MicOff from "lucide-react-native/icons/mic-off";
import Minus from "lucide-react-native/icons/minus";
import CircleMinus from "lucide-react-native/icons/circle-minus";
import Monitor from "lucide-react-native/icons/monitor";
import Moon from "lucide-react-native/icons/moon";
import Ellipsis from "lucide-react-native/icons/ellipsis";
import Paperclip from "lucide-react-native/icons/paperclip";
import Pin from "lucide-react-native/icons/pin";
import PinOff from "lucide-react-native/icons/pin-off";
import Pause from "lucide-react-native/icons/pause";
import Play from "lucide-react-native/icons/play";
import Plus from "lucide-react-native/icons/plus";
import Power from "lucide-react-native/icons/power";
import Radio from "lucide-react-native/icons/radio";
import RefreshCw from "lucide-react-native/icons/refresh-cw";
import Repeat from "lucide-react-native/icons/repeat";
import RotateCcw from "lucide-react-native/icons/rotate-ccw";
import RotateCw from "lucide-react-native/icons/rotate-cw";
import Save from "lucide-react-native/icons/save";
import Search from "lucide-react-native/icons/search";
import Send from "lucide-react-native/icons/send";
import Server from "lucide-react-native/icons/server";
import Settings from "lucide-react-native/icons/settings";
import Shield from "lucide-react-native/icons/shield";
import ShieldOff from "lucide-react-native/icons/shield-off";
import Shuffle from "lucide-react-native/icons/shuffle";
import SlidersHorizontal from "lucide-react-native/icons/sliders-horizontal";
import Smartphone from "lucide-react-native/icons/smartphone";
import Square from "lucide-react-native/icons/square";
import Star from "lucide-react-native/icons/star";
import Sun from "lucide-react-native/icons/sun";
import Table from "lucide-react-native/icons/table";
import Terminal from "lucide-react-native/icons/terminal";
import Wrench from "lucide-react-native/icons/wrench";
import Trash from "lucide-react-native/icons/trash";
import Type from "lucide-react-native/icons/type";
import Underline from "lucide-react-native/icons/underline";
import User from "lucide-react-native/icons/user";
import WifiOff from "lucide-react-native/icons/wifi-off";
import X from "lucide-react-native/icons/x";
import CircleX from "lucide-react-native/icons/circle-x";
import OctagonX from "lucide-react-native/icons/octagon-x";
import Zap from "lucide-react-native/icons/zap";

/**
 * The app's icons, drawn from the desktop's set.
 *
 * The renderer draws every icon from Lucide, and this is the same family at the
 * same version: a glyph the desktop shows is the glyph drawn here. Call sites
 * keep the names they already use, and the map records where Lucide renamed one,
 * and where two of our names land on the same drawing.
 */
export const glyphs = {
	"activity": Activity,
	"alert-circle": CircleAlert,
	"alert-triangle": TriangleAlert,
	"archive": Archive,
	"arrow-down": ArrowDown,
	"arrow-up": ArrowUp,
	"bell": Bell,
	"bookmark": Bookmark,
	"camera-off": CameraOff,
	"check": Check,
	"check-circle": CircleCheck,
	"circle-dashed-check": CircleDashedCheck,
	"check-square": SquareCheck,
	"chevron-down": ChevronDown,
	"chevron-left": ChevronLeft,
	"chevron-right": ChevronRight,
	"chevron-up": ChevronUp,
	"circle": Circle,
	"clock": Clock,
	"cloud": Cloud,
	"code": Code,
	"command": Command,
	"copy": Copy,
	"corner-down-left": CornerDownLeft,
	"corner-down-right": CornerDownRight,
	"corner-up-right": CornerUpRight,
	"cpu": Cpu,
	"delete": Delete,
	"disc": Disc,
	"download-cloud": CloudDownload,
	"edit-2": Pen,
	"edit-3": PenLine,
	"external-link": ExternalLink,
	"feather": FeatherGlyph,
	"file": File,
	"file-text": FileText,
	"folder": Folder,
	"folder-open": FolderOpen,
	"flag": Flag,
	"git-branch": GitBranch,
	"git-merge": GitMerge,
	"git-pull-request": GitPullRequest,
	"globe": Globe,
	"grid": Grid2x2,
	"help-circle": CircleQuestionMark,
	"image": Image,
	"inbox": Inbox,
	"info": Info,
	"italic": Italic,
	"key": KeyRound,
	"layers": Layers,
	"link": Link,
	"list": List,
	"loader": LoaderCircle,
	"log-out": LogOut,
	"map": Map,
	"maximize": Maximize,
	"menu": Menu,
	"message-circle": MessageCircle,
	"message-square": MessageSquare,
	"mic": Mic,
	"mic-off": MicOff,
	"minus": Minus,
	"minus-circle": CircleMinus,
	"monitor": Monitor,
	"moon": Moon,
	"more-horizontal": Ellipsis,
	"paperclip": Paperclip,
	"pin": Pin,
	"pin-off": PinOff,
	"pause": Pause,
	"play": Play,
	"plus": Plus,
	"power": Power,
	"radio": Radio,
	"refresh-cw": RefreshCw,
	"repeat": Repeat,
	"rotate-ccw": RotateCcw,
	"rotate-cw": RotateCw,
	"save": Save,
	"search": Search,
	"send": Send,
	"server": Server,
	"settings": Settings,
	"shield": Shield,
	"shield-off": ShieldOff,
	"shuffle": Shuffle,
	"sliders": SlidersHorizontal,
	"smartphone": Smartphone,
	"square": Square,
	"star": Star,
	"sun": Sun,
	"table": Table,
	"terminal": Terminal,
	"tool": Wrench,
	"trash": Trash,
	"trash-2": Trash,
	"type": Type,
	"underline": Underline,
	"user": User,
	"wifi-off": WifiOff,
	"x": X,
	"x-circle": CircleX,
	"x-octagon": OctagonX,
	"zap": Zap,
} as const;

export type FeatherIconName = keyof typeof glyphs;

/** Feather's calling convention, Lucide's drawings. */
function Icon({
	name,
	size = 24,
	color,
	style,
	...rest
}: {
	/**
	 * A known name autocompletes; a string is accepted because a few call sites
	 * carry an icon name out of data rather than a literal.
	 */
	name: FeatherIconName | (string & {}) | (number & {}) | (symbol & {});
	size?: number;
	color?: string;
	style?: ComponentProps<(typeof glyphs)[FeatherIconName]>["style"];
} & Omit<ComponentProps<(typeof glyphs)[FeatherIconName]>, "size" | "color" | "style">) {
	const Glyph = glyphs[name as FeatherIconName];
	if (!Glyph) return null;
	return <Glyph size={size} color={color} style={style} strokeWidth={2} {...rest} />;
}

/**
 * `Feather.glyphMap` is only ever read as a type by call sites that take an icon
 * name in their props; this keeps those signatures working unchanged.
 */
export const Feather = Object.assign(Icon, { glyphMap: {} as Record<string, number> });
