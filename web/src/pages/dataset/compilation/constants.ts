import { GenerateType } from '@/constants/knowledge';

export enum ViewMode {
  LlmWiki = 'llm-wiki',
  Skills = 'skills',
  Tree = 'tree',
  Graph = 'graph',
  MindMap = 'mindmap',
  Timeline = 'timeline',
}

export const VisibleViewModes = Object.values(ViewMode).filter(
  (mode) => mode !== ViewMode.Skills,
);

export enum LeftPanelTab {
  Contents = 'contents',
  Graph = 'graph',
}

export const StructureKinds = [
  ViewMode.Graph,
  ViewMode.MindMap,
  ViewMode.Timeline,
] as const;

export type StructureKind = (typeof StructureKinds)[number];

export const ViewModeLabelKeyMap: Record<ViewMode, string> = {
  [ViewMode.LlmWiki]: 'knowledgeCompilation.llmWiki',
  [ViewMode.Skills]: 'knowledgeCompilation.skills',
  [ViewMode.Tree]: 'knowledgeCompilation.navTree',
  [ViewMode.Graph]: 'knowledgeCompilation.graph',
  [ViewMode.MindMap]: 'knowledgeCompilation.structureMindmap',
  [ViewMode.Timeline]: 'knowledgeCompilation.structureTimeline',
};

export type GenerableViewMode = Exclude<ViewMode, ViewMode.Tree>;

export const ViewModeGenerateTypeMap: Record<GenerableViewMode, GenerateType> =
  {
    [ViewMode.LlmWiki]: GenerateType.Artifact,
    [ViewMode.Skills]: GenerateType.ToSkills,
    // Artifacts Graph/MindMap read artifacts/structure; generate must hit
    // structure_* merge (not classic GraphRAG type=graph / placeholder mindmap).
    [ViewMode.Graph]: GenerateType.StructureGraph,
    [ViewMode.MindMap]: GenerateType.StructureMindMap,
    [ViewMode.Timeline]: GenerateType.Timeline,
  };
