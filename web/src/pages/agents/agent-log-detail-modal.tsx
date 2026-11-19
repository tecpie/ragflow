import MessageItem from '@/components/next-message-item';
import { Modal } from '@/components/ui/modal/modal';
import { useFetchAgent } from '@/hooks/use-agent-request';
import { useFetchUserInfo } from '@/hooks/use-user-setting-request';
import {
  IAgentComponentIO,
  IAgentLogMessage,
} from '@/interfaces/database/agent';
import { IMessage } from '@/interfaces/database/chat';
import { buildMessageUuidWithRole, buildReferenceObject } from '@/utils/chat';
import { isEmpty } from 'lodash';
import React, { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { JsonViewer } from '../agent/form/components/json-viewer';

interface CustomModalProps {
  isOpen: boolean;
  onClose: () => void;
  message: IAgentLogMessage[];
}

function asRecord(data: unknown): Record<string, any> {
  if (data && typeof data === 'object' && !Array.isArray(data)) {
    return data as Record<string, any>;
  }
  return { value: data };
}

function ComponentIOBlock({ item }: { item: IAgentComponentIO }) {
  const { t } = useTranslation();
  const title = item.component_name || item.component_id;

  return (
    <div className="w-full rounded-md border border-border-button bg-bg-card p-3 space-y-3">
      <div className="text-sm font-medium text-text-primary">
        {item.component_type ? `${item.component_type} · ${title}` : title}
      </div>
      {!isEmpty(item.inputs) && (
        <JsonViewer data={asRecord(item.inputs)} title={t('flow.input')} />
      )}
      {!isEmpty(item.outputs) && (
        <JsonViewer data={asRecord(item.outputs)} title={t('flow.output')} />
      )}
    </div>
  );
}

export const AgentLogDetailModal: React.FC<CustomModalProps> = ({
  isOpen,
  onClose,
  message: derivedMessages,
}) => {
  const { t } = useTranslation();
  const { data: userInfo } = useFetchUserInfo();
  const { data: canvasInfo } = useFetchAgent();

  const shortMessage = useMemo(() => {
    if (derivedMessages?.length) {
      const content = derivedMessages[0]?.content || '';

      const chineseCharCount = (content.match(/[\u4e00-\u9fa5]/g) || []).length;
      const totalLength = content.length;

      if (chineseCharCount > 0) {
        if (totalLength > 15) {
          return content.substring(0, 15) + '...';
        }
      } else {
        if (totalLength > 30) {
          return content.substring(0, 30) + '...';
        }
      }
      return content;
    } else {
      return '';
    }
  }, [derivedMessages]);

  return (
    <Modal
      open={isOpen}
      onCancel={onClose}
      showfooter={false}
      footer={null}
      title={shortMessage || ''}
      className="!w-[900px]"
    >
      <div className="flex items-start mb-4 flex-col gap-4 justify-start">
        <div className="w-full space-y-4">
          {derivedMessages?.map((message, i) => {
            return (
              <div
                key={buildMessageUuidWithRole(message)}
                className="space-y-3"
              >
                <MessageItem
                  nickname={userInfo.nickname}
                  avatar={userInfo.avatar}
                  avatarDialog={canvasInfo.avatar}
                  item={message as IMessage}
                  reference={buildReferenceObject(message.reference)}
                  index={i}
                  showLikeButton={false}
                  showLog={false}
                ></MessageItem>
                {message.role === 'user' && !isEmpty(message.inputs) && (
                  <JsonViewer
                    data={asRecord(message.inputs)}
                    title={t('flow.input')}
                  />
                )}
                {message.role === 'assistant' && !isEmpty(message.outputs) && (
                  <JsonViewer
                    data={asRecord(message.outputs)}
                    title={t('flow.output')}
                  />
                )}
                {message.component_io?.map((item) => (
                  <ComponentIOBlock
                    key={`${message.id}-${item.component_id}`}
                    item={item}
                  />
                ))}
              </div>
            );
          })}
        </div>
      </div>
    </Modal>
  );
};
