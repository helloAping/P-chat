/**
 * Icon barrel — single import point for the lucide-vue-next icons
 * used across the P-Chat frontend. Components import named icons
 * from this file rather than reaching into `lucide-vue-next`
 * directly. Two reasons:
 *
 *   1. Tree-shaking stays effective: each component only pulls the
 *      icon module it references. The barrel is just a re-export.
 *   2. The set of icons we actually use is curated — new icons
 *      get added here on purpose, not by accident.
 *
 * Usage:
 *   import { Send, Paperclip, X } from './icons'
 *   <Send :size="16" />
 *
 * Icon sizing: lucide icons use 24×24 as default. We use these
 * conventions across the app:
 *   14 — inline with text (12–13px font)
 *   16 — button icons in toolbars, sidebar icons
 *   18 — input area icons (paperclip, send)
 *   20 — modal title icons, status badges
 *   24 — empty-state icons, hero icons
 *
 * Color: lucide icons inherit `currentColor` for stroke, so the
 * parent text color drives the icon color. No color prop needed
 * unless overriding.
 */
export {
  Send,
  Square,
  Paperclip,
  Search,
  X,
  Pencil,
  MoreHorizontal,
  Moon,
  Sun,
  Settings,
  FolderOpen,
  Terminal,
  Clipboard,
  Download,
  AlertTriangle,
  Check,
  Undo2,
  ArrowDown,
  ArrowUp,
  ArrowRight,
  Image as ImageIcon,
  Film,
  Volume2,
  FileText,
  File,
  FileCode,
  Lock,
  Unlock,
  Key,
  BarChart3,
  Bell,
  Info,
  Globe,
  Folder,
  Hammer,
  Wrench,
  Eye,
  Trash2,
  Loader2,
  ChevronRight,
  ChevronDown,
  ChevronUp,
  ChevronLeft,
  PanelLeft,
  PanelLeftClose,
  PanelLeftOpen,
  Circle,
  Dot,
  MessageSquare,
  Plus,
  Minus,
  HelpCircle,
  Sparkles,
  Bot,
  Lightbulb,
  User,
  Copy,
  RotateCcw,
  RotateCw,
  Star,
  Pin,
  PinOff,
  Hash,
  VolumeX,
  GitBranch,
  Cpu,
  Palette,
  Archive,
  Server,
  ExternalLink,
  Command,
  Database,
  Bookmark,
  CheckCircle2,
  XCircle,
  AlertCircle,
  Triangle,
  ShieldAlert,
  ShieldCheck,
  Monitor,
  CornerDownLeft,
} from 'lucide-vue-next'
