import {
  ApartmentOutlined,
  AppstoreOutlined,
  BankOutlined,
  BulbOutlined,
  ClockCircleOutlined,
  CodeOutlined,
  CompassOutlined,
  DatabaseOutlined,
  ExperimentOutlined,
  HomeOutlined,
  MessageOutlined,
  ReadOutlined,
  RobotOutlined,
  RocketOutlined,
  StarOutlined,
  TeamOutlined,
  ThunderboltOutlined,
  ToolOutlined,
} from '@ant-design/icons';

export const NAVIGATION_ICON_COMPONENTS = {
  home: HomeOutlined,
  message: MessageOutlined,
  robot: RobotOutlined,
  compass: CompassOutlined,
  bolt: ThunderboltOutlined,
  apps: AppstoreOutlined,
  workflow: ApartmentOutlined,
  book: ReadOutlined,
  clock: ClockCircleOutlined,
  building: BankOutlined,
  bulb: BulbOutlined,
  star: StarOutlined,
  experiment: ExperimentOutlined,
  code: CodeOutlined,
  database: DatabaseOutlined,
  team: TeamOutlined,
  tool: ToolOutlined,
  rocket: RocketOutlined,
} as const;

export type NavigationIconId = keyof typeof NAVIGATION_ICON_COMPONENTS;

const NAVIGATION_ICON_LABELS: Record<NavigationIconId, string> = {
  home: '首页',
  message: '消息',
  robot: '机器人',
  compass: '指南针',
  bolt: '闪电',
  apps: '应用网格',
  workflow: '工作流',
  book: '书本',
  clock: '时钟',
  building: '企业',
  bulb: '灵感',
  star: '星标',
  experiment: '实验',
  code: '代码',
  database: '数据库',
  team: '团队',
  tool: '工具',
  rocket: '火箭',
};

export const NAVIGATION_ICON_OPTIONS = (
  Object.keys(NAVIGATION_ICON_COMPONENTS) as NavigationIconId[]
).map((id) => ({ id, label: NAVIGATION_ICON_LABELS[id] }));

export function isNavigationIconId(value: unknown): value is NavigationIconId {
  return typeof value === 'string'
    && Object.prototype.hasOwnProperty.call(NAVIGATION_ICON_COMPONENTS, value);
}

export function getNavigationIconComponent(iconId: unknown): typeof HomeOutlined {
  return isNavigationIconId(iconId)
    ? NAVIGATION_ICON_COMPONENTS[iconId]
    : HomeOutlined;
}
