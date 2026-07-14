import { useFetchAllAddedModels } from '@/hooks/use-llm-request';
import { resolveModelRef } from '@/utils/llm-util';
import { memo, useMemo } from 'react';
import { LlmIcon } from '../svg-icon';

interface IProps {
  value?: string;
}

export const LLMLabel = ({ value }: IProps) => {
  const { data: allAddedModels } = useFetchAllAddedModels();
  const resolved = useMemo(
    () => resolveModelRef(value, allAddedModels),
    [allAddedModels, value],
  );

  if (!resolved?.model_name) return null;

  return (
    <div className="flex items-center gap-1.5 min-w-0">
      <LlmIcon
        name={resolved.model_provider}
        width={22}
        height={22}
        imgClass="size-[22px] flex-shrink-0"
      />
      <span className="font-medium truncate">{resolved.model_name}</span>
      {resolved.model_instance && (
        <span className="text-text-secondary truncate flex-shrink-0">
          {resolved.model_instance}
        </span>
      )}
    </div>
  );
};

export default memo(LLMLabel);
